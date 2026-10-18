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
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
)

// This runs the complete host gateway Run entrypoint. The control peer here is
// a signed tRPC protocol fixture; real Admin integration is a separate gate.
func TestRunOwnsTLSLocalDirectoryAndCompleteCache(t *testing.T) {
	ca := testcert.New(t, nil)
	credential := testsnapshot.Credential("host-gateway@storage")
	cfg := hostgatewayconfig.Default("storage", "control", "127.0.0.1", credential.KeyID)
	cfg.TLS = testcert.Files(t, ca, "storage", nil)
	cfg.Server.RemoteAddr, cfg.Server.LocalAddr, cfg.Server.HealthAddr = testrpc.Address(t), testrpc.Address(t), testrpc.Address(t)
	cfg.Store.Path = filepath.Join(t.TempDir(), "cache")
	cfg.Control.KeyFile = filepath.Join(t.TempDir(), "caller.key")
	require.NoError(t, os.WriteFile(cfg.Control.KeyFile, []byte(credential.Secret+"\n"), 0o600))
	controlTLS, err := tlsconfig.Load("control", testcert.Files(t, ca, "control", nil))
	require.NoError(t, err)
	controlSocket, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cfg.Control.Target = controlSocket.Addr().String()
	raw := testsnapshot.New(t, "storage", "storage-primary")
	reports := make(chan *pb.ReportStatusReq, 10)
	testrpc.Serve(t, tls.NewListener(controlSocket, controlTLS.Server()), servicecatalog.GatewayControlPath, func(ctx context.Context, body []byte) ([]byte, error) {
		message := codec.Message(ctx)
		method := strings.TrimPrefix(message.ServerRPCName(), "/"+servicecatalog.GatewayControlPath+"/")
		headers := http.Header{}
		for k, v := range message.ServerMetaData() {
			headers.Add(k, string(v))
		}
		_, err := gatewayauth.Verify(credential, gatewayauth.Request{Method: "POST", Path: message.ServerRPCName(), TargetNode: "control", Callee: servicecatalog.GatewayControlPath, Func: method, Body: body}, headers, time.Now())
		if err != nil {
			return nil, err
		}
		if method == "PullSnapshot" {
			req := &pb.PullSnapshotReq{}
			if err := proto.Unmarshal(body, req); err != nil {
				return nil, err
			}
			rsp := &pb.PullSnapshotRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}
			if req.CurrentHash != raw.Hash {
				rsp.Changed, rsp.Snapshot = true, raw
			}
			return proto.Marshal(rsp)
		}
		req := &pb.ReportStatusReq{}
		if err := proto.Unmarshal(body, req); err != nil {
			return nil, err
		}
		reports <- req
		return proto.Marshal(&pb.ReportStatusRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
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
	for key, value := range map[string]string{"MOOX_HEALTH_AUTH_VERSION": "moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY": "fixture", "MOOX_HEALTH_AUTH_SECRET_KEY": "synthetic-health", "MOOX_INSTANCE_ID": "storage", "MOOX_NODE_ID": "storage", "MOOX_BOOT_ID": "synthetic-boot", "MOOX_METRICS_EVENTBUS_URL": "nats://127.0.0.1:1"} {
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
	healthClient := &http.Client{Timeout: 2 * time.Second}
	healthURL := "http://" + cfg.Server.HealthAddr + "/readyz"
	unauthorized, err := healthClient.Get(healthURL)
	require.NoError(t, err)
	_ = unauthorized.Body.Close()
	require.Equal(t, http.StatusUnauthorized, unauthorized.StatusCode)
	healthRequest, err := http.NewRequest(http.MethodGet, healthURL, nil)
	require.NoError(t, err)
	timestamp, nonce := time.Now().Unix(), strings.Repeat("a", 64)
	signature, err := requestauth.Sign("synthetic-health", requestauth.Material{Method: http.MethodGet, Path: "/readyz", Timestamp: timestamp, Nonce: nonce})
	require.NoError(t, err)
	healthRequest.Header.Set("X-Moox-Health-Auth", fmt.Sprintf("moox-health-v1/fixture/%d/%s/%s", timestamp, nonce, signature))
	ready, err := healthClient.Do(healthRequest)
	require.NoError(t, err)
	_ = ready.Body.Close()
	require.Equal(t, http.StatusOK, ready.StatusCode)
	client, err := gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, LocalHostID: "storage", LocalAddress: cfg.Server.LocalAddr, CAFile: cfg.TLS.CAFile,
		Credentials: testsnapshot.Credential("collector"), Serialization: codec.SerializationTypePB, Timeout: 3 * time.Second})
	require.NoError(t, err)
	defer client.Close()
	object := &wrapperspb.StringValue{}
	require.NoError(t, client.Invoke(context.Background(), "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", wrapperspb.String("PB object"), object))
	require.Equal(t, "PB object", object.Value)
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
			headers, err := testrpc.Signed(testsnapshot.Credential("collector"), "storage", "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", payload.body)
			require.NoError(t, err)
			response, err := testrpc.Call(context.Background(), endpoint.address, endpoint.trust, "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", payload.serialization, payload.body, headers)
			require.NoError(t, err)
			require.Equal(t, payload.body, response)
			_, err = testrpc.Call(context.Background(), endpoint.address, endpoint.trust, "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", payload.serialization, payload.body, headers)
			require.ErrorContains(t, err, "replayed")
		}
	}
	before := calls.Load()
	for _, denied := range []struct{ caller, service, method string }{{"console", "trpc.moox.storage.PrimaryStore", "CollectGarbage"}, {"moox-skill", "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows"}, {"collector", "trpc.moox.ops.SecretMgr", "GetSecretValue"}} {
		headers, err := testrpc.Signed(testsnapshot.Credential(denied.caller), "storage", denied.service, denied.method, nil)
		require.NoError(t, err)
		_, err = testrpc.Call(context.Background(), cfg.Server.LocalAddr, nil, denied.service, denied.method, codec.SerializationTypePB, nil, headers)
		require.Error(t, err)
	}
	require.Equal(t, before, calls.Load())
	_, err = testrpc.Call(context.Background(), cfg.Server.RemoteAddr, trust, "trpc.moox.hostgateway.Directory", "GetDirectory", codec.SerializationTypePB, nil, nil)
	require.Error(t, err)
	view, err := store.NewSnapshots(cfg.Store.Path, "storage").Load()
	require.NoError(t, err)
	require.Equal(t, raw.Hash, view.Hash())
	_, err = snapshot.Build("storage", view.Proto())
	require.NoError(t, err)
	select {
	case report := <-reports:
		require.NotEmpty(t, report.InstanceId)
		require.Equal(t, "e2e-version", report.Version)
		require.Equal(t, raw.Hash, report.AppliedHash)
	case <-time.After(time.Second):
		t.Fatal("heartbeat missing")
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
