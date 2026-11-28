package test

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testcert"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testrpc"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
)

// This runs the complete host gateway Run entrypoint. The control peer here is
// a signed tRPC protocol fixture; real Admin integration is a separate gate.
func TestRunOwnsTLSLocalDirectoryAndCompleteCache(t *testing.T) {
	ca := testcert.New(t, nil)
	credential := testsnapshot.Credential("host-gateway", "storage")
	cfg := hostgatewayconfig.Default("storage", "control", "127.0.0.1")
	cfg.TLS = testcert.Files(t, ca, "storage", nil)
	cfg.Server.RemoteAddr, cfg.Server.LocalAddr, cfg.Server.HealthAddr = testrpc.Address(t), testrpc.Address(t), testrpc.Address(t)
	// 存储机登记成外部入口：网关多开一个明文监听。
	cfg.Server.ExternalAddr = testrpc.Address(t)
	cfg.Store.Path = filepath.Join(t.TempDir(), "cache")
	cfg.Control.KeyFile = filepath.Join(t.TempDir(), "host.key")
	require.NoError(t, os.WriteFile(cfg.Control.KeyFile, []byte(gatewayauth.EncodeSigningKey(credential.Key)+"\n"), 0o600))
	controlTLS, err := tlsconfig.Load("control", testcert.Files(t, ca, "control", nil))
	require.NoError(t, err)
	controlSocket, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cfg.Control.Target = controlSocket.Addr().String()
	raw := testsnapshot.New(t, "storage", "storage-primary", "monitor")
	testsnapshot.Host(t, raw, "storage").ExternalEntry = true
	testsnapshot.AddIdentity(raw, "scf-collector")
	testsnapshot.Rehash(t, raw)
	pulls := make(chan *pb.PullSnapshotReq, 10)
	testrpc.Serve(t, tls.NewListener(controlSocket, controlTLS.Server()), servicecatalog.GatewayControlPath, func(ctx context.Context, body []byte) ([]byte, error) {
		message := codec.Message(ctx)
		method := strings.TrimPrefix(message.ServerRPCName(), "/"+servicecatalog.GatewayControlPath+"/")
		headers := http.Header{}
		for k, v := range message.ServerMetaData() {
			headers.Add(k, string(v))
		}
		_, err := authtest.Verify(credential, gatewayauth.Request{TargetNode: "control", Callee: servicecatalog.GatewayControlPath, Func: method, Body: body}, headers, time.Now())
		if err != nil {
			return nil, err
		}
		if method != "PullSnapshot" {
			return nil, fmt.Errorf("unexpected GatewayControl method %s", method)
		}
		req := &pb.PullSnapshotReq{}
		if err := proto.Unmarshal(body, req); err != nil {
			return nil, err
		}
		select {
		case pulls <- req:
		default:
		}
		rsp := &pb.PullSnapshotRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}
		if req.CurrentHash != raw.Hash {
			rsp.Changed, rsp.Snapshot = true, raw
		}
		return proto.Marshal(rsp)
	})
	upstream, err := net.Listen("tcp", "127.0.0.1:20102")
	require.NoError(t, err)
	var calls atomic.Int32
	testrpc.Serve(t, upstream, "trpc.moox.storage.PrimaryStore", func(ctx context.Context, body []byte) ([]byte, error) {
		calls.Add(1)
		for key := range codec.Message(ctx).ServerMetaData() {
			require.False(t, strings.HasPrefix(strings.ToLower(key), "x-moox-"), "gateway authentication must end at the gateway")
		}
		return body, nil
	})
	for key, value := range map[string]string{"MOOX_INSTANCE_ID": "storage", "MOOX_NODE_ID": "storage", "MOOX_BOOT_ID": "synthetic-boot", "MOOX_METRICS_EVENTBUS_URL": "nats://127.0.0.1:1"} {
		t.Setenv(key, value)
	}
	oldPath := trpc.ServerConfigPath
	trpc.ServerConfigPath, err = filepath.Abs("../config/trpc_go.yaml")
	require.NoError(t, err)
	t.Cleanup(func() { trpc.ServerConfigPath = oldPath })
	configFile := filepath.Join(t.TempDir(), "app.yaml")
	encoded, err := hostgatewayconfig.Encode(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configFile, encoded, 0o600))
	executable, err := os.Executable()
	require.NoError(t, err)
	process := exec.Command(executable, "-test.run=^TestGatewayProcess$")
	process.Env = append(os.Environ(), "MOOX_HOST_GATEWAY_TEST_CONFIG="+configFile, "MOOX_HOST_GATEWAY_TEST_TRPC="+trpc.ServerConfigPath)
	input, err := process.StdinPipe()
	require.NoError(t, err)
	var output bytes.Buffer
	process.Stdout, process.Stderr = &output, &output
	require.NoError(t, process.Start())
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	defer func() {
		_ = input.Close()
		select {
		case err := <-done:
			require.NoError(t, err, "%s", output.String())
		case <-time.After(10 * time.Second):
			_ = process.Process.Kill()
			<-done
			t.Error("host gateway did not stop")
		}
	}()
	source, err := gatewayclient.NewLocalDirectorySource(cfg.Server.LocalAddr, time.Second)
	require.NoError(t, err)
	defer source.Close()
	require.Eventually(t, func() bool { _, err := source.Fetch(context.Background(), ""); return err == nil }, 10*time.Second, 30*time.Millisecond)
	// 健康端口只听本机、不验签：同机直接请求即可。
	healthClient := &http.Client{Timeout: 2 * time.Second}
	ready, err := healthClient.Get("http://" + cfg.Server.HealthAddr + "/readyz")
	require.NoError(t, err)
	_ = ready.Body.Close()
	require.Equal(t, http.StatusOK, ready.StatusCode)
	// 本机的调用方（storage 上的 monitor）从本机网关拿目录，目标服务就在本机：直连它的回环端口，不经网关。
	client, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, LocalHostID: "storage", LocalAddress: cfg.Server.LocalAddr, CAFile: cfg.TLS.CAFile,
		Credentials: testsnapshot.Credential("monitor", "storage"), Serialization: codec.SerializationTypePB, Timeout: 3 * time.Second})
	require.NoError(t, err)
	defer client.Close()
	require.Equal(t, []string{"storage"}, client.Directory().Services["trpc.moox.storage.PrimaryStore"], "客户端按内嵌目录把主机与部署展开")
	require.Contains(t, client.Directory().Hosts, "control")
	object := &wrapperspb.StringValue{}
	require.NoError(t, client.Invoke(context.Background(), "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", wrapperspb.String("PB object"), object))
	require.Equal(t, "PB object", object.Value)
	require.EqualValues(t, 1, calls.Load())
	// 直连不查方法名单：monitor 不在 RegisterSeriesSnapshot 的名单里，经网关会被拒（见下），同机直连照样到达服务。
	headers, err := testrpc.Signed(testsnapshot.Credential("monitor", "storage"), "storage", "trpc.moox.storage.PrimaryStore", "RegisterSeriesSnapshot", nil)
	require.NoError(t, err)
	_, err = testrpc.Call(context.Background(), cfg.Server.LocalAddr, nil, "trpc.moox.storage.PrimaryStore", "RegisterSeriesSnapshot", codec.SerializationTypePB, nil, headers)
	require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "monitor 经网关调不了 RegisterSeriesSnapshot")
	require.EqualValues(t, 1, calls.Load())
	object.Reset()
	require.NoError(t, client.Invoke(context.Background(), "trpc.moox.storage.PrimaryStore", "RegisterSeriesSnapshot", wrapperspb.String("direct"), object))
	require.Equal(t, "direct", object.Value)
	require.EqualValues(t, 2, calls.Load())
	// 别的主机上的调用方（control 上的 collector）经网关：跨主机端口走 TLS，签名、防重放、调用名单都在网关上。
	material, err := tlsconfig.Load("storage", cfg.TLS)
	require.NoError(t, err)
	trust, err := material.Client("storage")
	require.NoError(t, err)
	for _, endpoint := range []struct {
		address string
		trust   *tls.Config
	}{{cfg.Server.LocalAddr, nil}, {cfg.Server.RemoteAddr, trust}} {
		for _, payload := range []struct {
			serialization int
			body          []byte
		}{{codec.SerializationTypePB, []byte{0x0a, 0x01, 'a', 0x0a, 0x01, 'b'}}, {codec.SerializationTypeJSON, []byte(" { \"x\": 1 }\n")}} {
			headers, err := testrpc.Signed(testsnapshot.Credential("collector", "control"), "storage", "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", payload.body)
			require.NoError(t, err)
			response, err := testrpc.Call(context.Background(), endpoint.address, endpoint.trust, "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", payload.serialization, payload.body, headers)
			require.NoError(t, err)
			require.Equal(t, payload.body, response)
			_, err = testrpc.Call(context.Background(), endpoint.address, endpoint.trust, "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", payload.serialization, payload.body, headers)
			require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "同一个签名第二次出现")
		}
	}
	before := calls.Load()
	for _, denied := range []struct{ caller, service, method string }{{"trade", "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows"}, {"scf-collector", "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch"}, {"collector", "trpc.moox.ops.SecretMgr", "GetSecretValue"}} {
		host := "control"
		if denied.caller == "scf-collector" {
			host = ""
		}
		headers, err := testrpc.Signed(testsnapshot.Credential(denied.caller, host), "storage", denied.service, denied.method, nil)
		require.NoError(t, err)
		_, err = testrpc.Call(context.Background(), cfg.Server.LocalAddr, nil, denied.service, denied.method, codec.SerializationTypePB, nil, headers)
		require.Error(t, err)
	}
	require.Equal(t, before, calls.Load())
	// 外部入口：外部身份调名单上的方法转到本机服务；名单外的方法、组件的签名都在入口被拒；外部入口上没有目录服务。
	scf := testsnapshot.Credential("scf-collector", "")
	entryBody := []byte{0x0a, 0x01, 'e'}
	headers, err = testrpc.Signed(scf, "storage", "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", entryBody)
	require.NoError(t, err)
	response, err := testrpc.Call(context.Background(), cfg.Server.ExternalAddr, nil, "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", codec.SerializationTypePB, entryBody, headers)
	require.NoError(t, err)
	require.Equal(t, entryBody, response)
	require.Equal(t, before+1, calls.Load())
	for _, denied := range []struct {
		credentials gatewayauth.Credentials
		method      string
	}{{scf, "ReadTimeSeriesRows"}, {testsnapshot.Credential("collector", "control"), "ReadTimeSeriesRows"}} {
		headers, err := testrpc.Signed(denied.credentials, "storage", "trpc.moox.storage.PrimaryStore", denied.method, nil)
		require.NoError(t, err)
		_, err = testrpc.Call(context.Background(), cfg.Server.ExternalAddr, nil, "trpc.moox.storage.PrimaryStore", denied.method, codec.SerializationTypePB, nil, headers)
		require.EqualValues(t, gatewayauth.RetRejected, errs.Code(err), "%s 经外部入口调 %s", denied.credentials.Caller, denied.method)
	}
	_, err = testrpc.Call(context.Background(), cfg.Server.ExternalAddr, nil, "trpc.moox.hostgateway.Directory", "GetDirectory", codec.SerializationTypePB, nil, nil)
	require.Error(t, err)
	require.Equal(t, before+1, calls.Load())
	_, err = testrpc.Call(context.Background(), cfg.Server.RemoteAddr, trust, "trpc.moox.hostgateway.Directory", "GetDirectory", codec.SerializationTypePB, nil, nil)
	require.Error(t, err)
	view, err := store.NewSnapshots(cfg.Store.Path, "storage").Load()
	require.NoError(t, err)
	require.Equal(t, raw.Hash, view.Hash())
	_, err = snapshot.Build("storage", view.Proto())
	require.NoError(t, err)
	// 之后的拉取带上已应用的快照哈希（兼作心跳）。
	deadline := time.After(10 * time.Second)
	for applied := false; !applied; {
		select {
		case req := <-pulls:
			applied = req.CurrentHash == raw.Hash
		case <-deadline:
			t.Fatal("pull with applied hash missing")
		}
	}
}

// Each gateway owns a process because tRPC startup initializes global state.
// The parent closes stdin to request a portable, graceful shutdown.
func TestGatewayProcess(t *testing.T) {
	path := os.Getenv("MOOX_HOST_GATEWAY_TEST_CONFIG")
	if path == "" {
		return
	}
	cfg, err := hostgatewayconfig.Load(path)
	require.NoError(t, err)
	trpc.ServerConfigPath = os.Getenv("MOOX_HOST_GATEWAY_TEST_TRPC")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); cancel() }()
	require.NoError(t, bootstrap.Run(ctx, cfg, "e2e-version"))
}
