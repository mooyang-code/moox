package test

import (
	"context"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/gateway"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
)

// forwardingSSHClient 模拟 SSH 端口转发：ForwardLocal 返回的本地端口把连接原样转到 e2e-helper。
type forwardingSSHClient struct {
	t       *testing.T
	gateway string
}

func (c forwardingSSHClient) Check(context.Context) error { return nil }

func (c forwardingSSHClient) ForwardLocal(_ context.Context, remote string) (net.Listener, error) {
	require.Equal(c.t, "127.0.0.1:11002", remote, "隧道应当转发到主机网关的本机入口")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", c.gateway)
			if err != nil {
				_ = local.Close()
				continue
			}
			go func() { _, _ = io.Copy(upstream, local); _ = upstream.Close() }()
			go func() { _, _ = io.Copy(local, upstream); _ = local.Close() }()
		}
	}()
	return listener, nil
}

func (forwardingSSHClient) Upload(context.Context, io.Reader, int64, string, fs.FileMode) error {
	return nil
}

func (forwardingSSHClient) Download(context.Context, string, io.Writer) (int64, error) {
	return 0, nil
}

func (forwardingSSHClient) Run(context.Context, []string, io.Reader) (setupssh.Result, error) {
	return setupssh.Result{}, nil
}

func (forwardingSSHClient) Close() error { return nil }

func TestMooxCLITunnelReachesStorageThroughControlGateway(t *testing.T) {
	storage := &klineStorageStub{requests: make(chan *pb.ReadTimeSeriesRowsReq, 1)}
	storageAddress := startKlineStorage(t, storage)

	helper := buildGatewayE2EHelper(t)
	tempDir := t.TempDir()
	readyFile := filepath.Join(tempDir, "gateway.ready")
	const secret = "moox-cli-tunnel-secret"
	process := startGatewayHelperProcessWithKeys(t, helper, "moox-cli:moox-cli-1:"+secret,
		"-host-id", "control",
		"-route", "trpc.moox.storage.PrimaryStore="+storageAddress,
		"-callers", "moox-cli",
		"-ready-file", readyFile,
		"-nonce-dir", filepath.Join(tempDir, "nonces"),
	)
	t.Cleanup(func() { process.stop(5 * time.Second) })
	target, err := process.waitForReady(readyFile, 30*time.Second)
	require.NoError(t, err)

	keyFile := filepath.Join(tempDir, "caller-moox-cli.key")
	raw, err := gatewayauth.MarshalCallerKey(gatewayauth.CallerKey{Caller: "moox-cli", KeyID: "moox-cli-1", Secret: secret})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyFile, raw, 0o600))
	t.Setenv(gateway.EnvKeyFile, keyFile)
	t.Setenv("HOME", tempDir)

	dialed := map[string]int{}
	client, err := gateway.NewWithDial(func(_ context.Context, hostID string) (setupssh.Client, error) {
		dialed[hostID]++
		return forwardingSSHClient{t: t, gateway: strings.TrimPrefix(target, "ip://")}, nil
	})
	require.NoError(t, err)
	defer client.Close()

	proxy := pb.NewPrimaryStoreClientProxy(client.ClientOptions()...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = proxy.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: klineStorageAppID, AppKey: klineStorageAppKey}, SpaceId: "crypto", DatasetId: "dataset_binance_kline_1m",
	})
	require.NoError(t, err)
	select {
	case req := <-storage.requests:
		require.Equal(t, "dataset_binance_kline_1m", req.GetDatasetId())
	case <-time.After(3 * time.Second):
		t.Fatal("Storage 没有收到经隧道转发的请求")
	}
	require.Equal(t, map[string]int{"control": 1}, dialed, "服务目录与请求共用到 control 的一条隧道")
}
