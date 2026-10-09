package test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	klineGatewayNode   = "gateway-kline-e2e"
	klineGatewayKeyID  = "moox-skill-e2e"
	klineGatewaySecret = "gateway-kline-e2e-secret-0123456789"
	klineStorageAppID  = "moox-skill"
	klineStorageAppKey = "storage-kline-e2e-app-key"
)

func TestKlineRPCUsesNativeGatewayHMACACLAndStorageAuth(t *testing.T) {
	storage := &klineStorageStub{requests: make(chan *pb.ReadTimeSeriesRowsReq, 4)}
	storageAddress := startKlineStorage(t, storage)
	gatewayTarget := startKlineNativeGateway(t, storageAddress)
	configPath := writeKlineConfig(t)
	operatorHome, manifestPath := writeKlineOperator(t, gatewayTarget)

	binary := buildMooxCLI(t)
	command := exec.Command(binary, "data", "kline", "get",
		"--config", configPath,
		"--file", manifestPath,
		"--data-type", "crypto",
		"--symbol", "BTC-USDT",
		"--interval", "1m",
		"--limit", "1",
	)
	command.Env = append(os.Environ(), "HOME="+operatorHome)
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.Output()
	require.NoError(t, err, "kline CLI diagnostics: %s", diagnostics.String())
	require.NotContains(t, diagnostics.String(), klineGatewaySecret)
	require.NotContains(t, diagnostics.String(), klineStorageAppKey)
	require.NotContains(t, string(output), klineGatewaySecret)
	require.NotContains(t, string(output), klineStorageAppKey)
	var response pb.ReadTimeSeriesRowsRsp
	require.NoError(t, protojson.Unmarshal(output, &response))
	require.Len(t, response.GetRows(), 1)
	require.Equal(t, "BTC-USDT", response.GetRows()[0].GetKey().GetSubjectId())
	require.Equal(t, "2026-08-28T12:34:00Z", response.GetRows()[0].GetKey().GetDataTime())
	require.True(t, response.GetComplete())

	select {
	case req := <-storage.requests:
		require.Equal(t, klineStorageAppID, req.GetAuthInfo().GetAppId())
		require.Equal(t, klineStorageAppKey, req.GetAuthInfo().GetAppKey())
		require.Equal(t, "crypto", req.GetSpaceId())
		require.Equal(t, "dataset_binance_kline_1m", req.GetDatasetId())
		require.Len(t, req.GetSelectors(), 1)
		require.Equal(t, "BTC-USDT", req.GetSelectors()[0].GetSubjectId())
		require.Equal(t, "venue:binance", req.GetSelectors()[0].GetSeriesTag())
	case <-time.After(3 * time.Second):
		t.Fatal("storage did not receive ReadTimeSeriesRows")
	}

	credentials := gatewayauth.Credentials{KeyID: klineGatewayKeyID, Caller: "moox-skill", Secret: klineGatewaySecret}
	proxy := pb.NewPrimaryStoreClientProxy(gatewayauth.NewTRPCClientOptions(gatewayTarget, klineGatewayNode, credentials)...)
	writeContext, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelWrite()
	_, err = proxy.UpsertFields(writeContext, &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: klineStorageAppID, AppKey: klineStorageAppKey},
	})
	require.Error(t, err)
	// An external principal cannot borrow the operator signing identity.
	require.NotContains(t, err.Error(), klineGatewaySecret)
	require.Equal(t, int32(0), storage.writeCalls.Load(), "write RPC must be rejected before reaching Storage")
}

func TestKlineRPCHelperEarlyExitRemainsObservableDuringCleanup(t *testing.T) {
	helper := buildGatewayE2EHelper(t)
	readyFile := filepath.Join(t.TempDir(), "never.ready")
	process := startGatewayHelperProcess(t, helper,
		"--mode", "kline-native",
		"--node-id", klineGatewayNode,
		"--upstream-addr", "not-an-address",
		"--ready-file", readyFile,
		"--nonce-dir", filepath.Join(t.TempDir(), "nonces"),
		"--key-id", klineGatewayKeyID,
	)

	select {
	case <-process.waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("invalid helper did not exit")
	}
	firstErr := process.waitError()
	require.Error(t, firstErr)
	require.Contains(t, process.logs.String(), "fixture upstream must be literal loopback")
	_, readyErr := process.waitForReady(readyFile, 2*time.Second)
	require.ErrorContains(t, readyErr, firstErr.Error())
	require.ErrorContains(t, readyErr, "fixture upstream must be literal loopback")

	cleanupDone := make(chan struct{})
	go func() {
		process.stop(time.Second)
		process.stop(time.Second)
		close(cleanupDone)
	}()
	select {
	case <-cleanupDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup blocked after helper exit was already observed")
	}
	require.Equal(t, firstErr, process.waitError())
}

type klineStorageStub struct {
	pb.UnimplementedPrimaryStore
	requests   chan *pb.ReadTimeSeriesRowsReq
	writeCalls atomic.Int32
}

func (stub *klineStorageStub) ReadTimeSeriesRows(_ context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	stub.requests <- req
	if req.GetAuthInfo().GetAppId() != klineStorageAppID || req.GetAuthInfo().GetAppKey() != klineStorageAppKey {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_NO_AUTH, Msg: "invalid storage auth"}}, nil
	}
	return &pb.ReadTimeSeriesRowsRsp{
		RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		Rows: []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "dataset_binance_kline_1m", SubjectId: "BTC-USDT",
			Freq: "1m", SeriesTag: "venue:binance", DataTime: "2026-08-28T12:34:00Z",
		}}},
		Complete: true,
	}, nil
}

func (stub *klineStorageStub) UpsertFields(context.Context, *pb.PrimaryUpsertFieldsReq) (*pb.PrimaryUpsertFieldsRsp, error) {
	stub.writeCalls.Add(1)
	return &pb.PrimaryUpsertFieldsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}

func startKlineStorage(t *testing.T, stub *klineStorageStub) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(
		server.WithNetwork("tcp"),
		server.WithProtocol("trpc"),
		server.WithServiceName("trpc.moox.storage.PrimaryStore"),
		server.WithListener(listener),
	)
	pb.RegisterPrimaryStoreService(service, stub)
	serveErr := make(chan error, 1)
	go func() { serveErr <- service.Serve() }()
	t.Cleanup(func() {
		service.Close(nil)
		select {
		case <-serveErr:
		case <-time.After(3 * time.Second):
			t.Error("storage server did not stop")
		}
	})
	return listener.Addr().String()
}

func startKlineNativeGateway(t *testing.T, upstreamAddress string) string {
	t.Helper()
	tempDir := t.TempDir()
	helper := buildGatewayE2EHelper(t)
	readyFile := filepath.Join(tempDir, "gateway.ready")
	process := startGatewayHelperProcess(t, helper,
		"--mode", "kline-native",
		"--node-id", klineGatewayNode,
		"--upstream-addr", upstreamAddress,
		"--ready-file", readyFile,
		"--nonce-dir", filepath.Join(tempDir, "nonces"),
		"--key-id", klineGatewayKeyID,
	)
	t.Cleanup(func() {
		if process.stop(5 * time.Second) {
			t.Errorf("gateway helper required kill: %s", process.logs.String())
		}
	})
	target, err := process.waitForReady(readyFile, 30*time.Second)
	require.NoError(t, err)
	return target
}

func buildGatewayE2EHelper(t *testing.T) string {
	t.Helper()
	if binary := prebuiltE2EBinary(t, "MOOX_CLI_GATEWAY_HELPER_BINARY"); binary != "" {
		return binary
	}
	helper := filepath.Join(t.TempDir(), "gateway-e2e-helper")
	build := exec.Command("go", "build", "-o", helper, "./cmd/e2e-helper")
	build.Dir = filepath.Join("..", "..", "hostgateway")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "build gateway helper: %s", output)
	return helper
}

type gatewayHelperProcess struct {
	command  *exec.Cmd
	logs     lockedBuffer
	waitDone chan struct{}
	waitMu   sync.Mutex
	waitErr  error
}

func startGatewayHelperProcess(t *testing.T, helper string, args ...string) *gatewayHelperProcess {
	t.Helper()
	process := &gatewayHelperProcess{
		command:  exec.Command(helper, args...),
		waitDone: make(chan struct{}),
	}
	process.command.Env = append(os.Environ(), "MOOX_GATEWAY_E2E_SERVICE_SECRET="+klineGatewaySecret)
	process.command.Stdout = &process.logs
	process.command.Stderr = &process.logs
	require.NoError(t, process.command.Start())
	go func() {
		err := process.command.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.waitDone)
	}()
	return process
}

func (process *gatewayHelperProcess) waitError() error {
	<-process.waitDone
	process.waitMu.Lock()
	defer process.waitMu.Unlock()
	return process.waitErr
}

// stop is idempotent. Its closed waitDone channel remains observable after any
// waiter sees the process exit, unlike consuming a single result from a channel.
func (process *gatewayHelperProcess) stop(timeout time.Duration) (killed bool) {
	select {
	case <-process.waitDone:
		return false
	default:
	}
	_ = process.command.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-process.waitDone:
		return false
	case <-timer.C:
		_ = process.command.Process.Kill()
	}
	timer.Reset(timeout)
	select {
	case <-process.waitDone:
	case <-timer.C:
	}
	return true
}

func (process *gatewayHelperProcess) waitForReady(path string, timeout time.Duration) (string, error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-process.waitDone:
			return "", fmt.Errorf("gateway helper exited before ready (%v): %s", process.waitError(), process.logs.String())
		case <-ticker.C:
			raw, err := os.ReadFile(path)
			if err == nil && strings.TrimSpace(string(raw)) != "" {
				return strings.TrimSpace(string(raw)), nil
			}
		case <-timer.C:
			return "", fmt.Errorf("gateway helper not ready: %s", process.logs.String())
		}
	}
}

type lockedBuffer struct {
	mu      sync.Mutex
	content strings.Builder
}

func (buffer *lockedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.content.Write(value)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.content.String()
}

func writeKlineConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data-access.yaml")
	content := fmt.Sprintf(`version: 1
storage:
  app_id: %q
  app_key: %q
data_types:
  crypto:
    default_exchange: binance
    exchanges:
      binance:
        space_id: crypto
        series_tag: venue:binance
        kline_datasets:
          1m: dataset_binance_kline_1m
`, klineStorageAppID, klineStorageAppKey)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func writeKlineOperator(t *testing.T, gatewayTarget string) (string, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	directory := filepath.Join(home, ".config", "moox")
	require.NoError(t, os.MkdirAll(directory, 0700))
	knownHosts := filepath.Join(directory, "known_hosts")
	require.NoError(t, os.WriteFile(knownHosts, nil, 0600))
	host := testfixture.GatewaySSH(t, klineGatewayNode, strings.TrimPrefix(gatewayTarget, "ip://"), knownHosts)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "caller-moox-cli.key"), []byte(klineGatewaySecret+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "gateway-client.yaml"), []byte("caller: moox-cli\nkey_id: "+klineGatewayKeyID+"\nkey_file: caller-moox-cli.key\n"), 0600))
	manifest := filepath.Join(home, "moox.toml")
	raw := fmt.Sprintf("[admin]\nusername='admin'\npassword='admin-test'\n[tencent_cloud]\nsecret_id='AKID-test'\nsecret_key='cloud-test'\n[eventbus]\nhost='127.0.0.1'\nport=4222\ntls_enabled=true\n[hosts.\"127.0.0.1\"]\nport=%d\nusername='%s'\npassword='%s'\n[control_host]\nname='%s'\nhost='127.0.0.1'\n", host.Port, host.Username, host.Password, host.Name)
	require.NoError(t, os.WriteFile(manifest, []byte(raw), 0600))
	return home, manifest
}
