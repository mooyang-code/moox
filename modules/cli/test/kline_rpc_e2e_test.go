package test

import (
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

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	klineGatewayNode   = "gateway-kline-e2e"
	klineGatewayKeyID  = "moox-skill-e2e"
	klineGatewaySecret = "gateway-kline-e2e-secret"
	klineStorageAppID  = "moox-skill"
	klineStorageAppKey = "storage-kline-e2e-app-key"
)

func TestKlineRPCUsesNativeGatewayHMACACLAndStorageAuth(t *testing.T) {
	storage := &klineStorageStub{requests: make(chan *pb.ReadTimeSeriesRowsReq, 4)}
	storageAddress := startKlineStorage(t, storage)
	gatewayTarget := startKlineNativeGateway(t, storageAddress)
	configPath := writeKlineConfig(t, gatewayTarget)

	binary := buildMooxCLI(t)
	command := exec.Command(binary, "data", "kline", "get",
		"--config", configPath,
		"--data-type", "crypto",
		"--symbol", "BTC-USDT",
		"--interval", "1m",
		"--limit", "1",
	)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "kline CLI output: %s", output)
	require.NotContains(t, string(output), klineGatewaySecret)
	require.NotContains(t, string(output), klineStorageAppKey)
	require.Contains(t, string(output), `"subject_id":  "BTC-USDT"`)
	require.Contains(t, string(output), `"data_time":  "2026-08-28T12:34:00Z"`)

	select {
	case req := <-storage.requests:
		require.Equal(t, klineStorageAppID, req.GetAuthInfo().GetAppId())
		require.Equal(t, klineStorageAppKey, req.GetAuthInfo().GetAppKey())
		require.Equal(t, "crypto_market", req.GetSpaceId())
		require.Equal(t, "binance_spot_kline_1m", req.GetDatasetId())
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
	require.Contains(t, err.Error(), "route not found")
	require.Equal(t, int32(0), storage.writeCalls.Load(), "write RPC must be rejected before reaching Storage")
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
			SpaceId: "crypto_market", DatasetId: "binance_spot_kline_1m", SubjectId: "BTC-USDT",
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
	helper := filepath.Join(tempDir, "gateway-e2e-helper")
	build := exec.Command("go", "build", "-o", helper, "./cmd/e2e-helper")
	build.Dir = filepath.Join("..", "..", "gateway")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "build gateway helper: %s", output)

	readyFile := filepath.Join(tempDir, "gateway.ready")
	command := exec.Command(helper,
		"--mode", "kline-native",
		"--node-id", klineGatewayNode,
		"--upstream-addr", upstreamAddress,
		"--ready-file", readyFile,
		"--nonce-dir", filepath.Join(tempDir, "nonces"),
		"--key-id", klineGatewayKeyID,
	)
	command.Env = append(os.Environ(), "MOOX_GATEWAY_E2E_SERVICE_SECRET="+klineGatewaySecret)
	var logs lockedBuffer
	command.Stdout = &logs
	command.Stderr = &logs
	require.NoError(t, command.Start())
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		if command.Process == nil {
			return
		}
		select {
		case <-done:
			return
		default:
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Errorf("gateway helper required kill: %s", logs.String())
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(readyFile)
		if readErr == nil && strings.TrimSpace(string(raw)) != "" {
			return strings.TrimSpace(string(raw))
		}
		select {
		case waitErr := <-done:
			t.Fatalf("gateway helper exited before ready (%v): %s", waitErr, logs.String())
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("gateway helper not ready: %s", logs.String())
	return ""
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

func writeKlineConfig(t *testing.T, gatewayTarget string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data-access.yaml")
	content := fmt.Sprintf(`version: 1
gateway:
  target: %q
  target_node: %q
  key_id: %q
  caller: moox-skill
  secret: %q
storage:
  app_id: %q
  app_key: %q
data_types:
  crypto:
    default_exchange: binance
    exchanges:
      binance:
        space_id: crypto_market
        series_tag: venue:binance
        kline_datasets:
          1m: binance_spot_kline_1m
`, gatewayTarget, klineGatewayNode, klineGatewayKeyID, klineGatewaySecret, klineStorageAppID, klineStorageAppKey)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}
