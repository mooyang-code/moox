//go:build e2e_external

package tradeowner

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 由 scripts/test/e2e/test-strategy-trade-gateway-e2e.sh 驱动：真实的共享网关客户端 → 目录 → 私有 CA 的 TLS 原生主机网关 → Trade。
func TestExternalStrategyGatewayOwnerClient(t *testing.T) {
	coord := os.Getenv("MOOX_GATEWAY_OWNER_E2E_COORD")
	require.NotEmpty(t, coord)
	read := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(coord, name))
		require.NoError(t, err)
		return strings.TrimSpace(string(raw))
	}
	endpoint := read("gateway-ready")
	host, port, err := net.SplitHostPort(endpoint)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host)
	require.Equal(t, "11003", port)
	credentials := gatewayauth.Credentials{Caller: "strategy", KeyID: read("caller-key-id"), Secret: read("caller-key")}
	open := func(caFile string, credentials gatewayauth.Credentials) (*gatewayclient.Client, error) {
		return gatewayclient.New(gatewayclient.Config{Mode: gatewayclient.Internal, Credentials: credentials, LocalHostID: "control", LocalAddress: read("directory-ready"), CAFile: caFile, CachePath: filepath.Join(t.TempDir(), "directory.json")})
	}
	gateway, err := open(filepath.Join(coord, "gateway-ca.pem"), credentials)
	require.NoError(t, err)
	defer gateway.Close()
	trade := New(Config{Timeout: 3 * time.Second}, gateway)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	const space, instance, session, startedAt = "space-gateway-e2e", "gateway-instance", "gateway-session", int64(1_700_000_000_000)
	id := read("logical-id")
	// 没有可信 CA、CA 不匹配或签名密钥错误时，调用都必须失败。
	_, err = open("", credentials)
	require.ErrorContains(t, err, "private CA")
	untrusted, err := open(filepath.Join(coord, "unrelated-ca.pem"), credentials)
	require.NoError(t, err)
	require.Error(t, New(Config{}, untrusted).CheckAccount(ctx, space, id, []string{"spot"}))
	require.NoError(t, untrusted.Close())
	wrongCredentials := credentials
	wrongCredentials.Secret = "wrong-e2e-secret"
	wrong, err := open(filepath.Join(coord, "gateway-ca.pem"), wrongCredentials)
	require.NoError(t, err)
	require.Error(t, New(Config{}, wrong).CloseSession(ctx, space, id, instance, session, startedAt))
	require.NoError(t, wrong.Close())
	// 启用前读账户核对、停用后关闭会话（重复关闭幂等），以及空间隔离。
	require.NoError(t, trade.CheckAccount(ctx, space, id, []string{"spot"}))
	require.ErrorIs(t, trade.CheckAccount(ctx, space, id, []string{"swap"}), ErrMarketMismatch)
	require.Error(t, trade.CheckAccount(ctx, "another-space", id, []string{"spot"}))
	require.NoError(t, trade.CloseSession(ctx, space, id, instance, session, startedAt))
	require.NoError(t, trade.CloseSession(ctx, space, id, instance, session, startedAt))
	// ACL：strategy 不能调用 Trade 的下单方法，也不能再认领或释放归属。
	for _, method := range []string{"SubmitOrder", "ClaimLogicalAccountOwner", "ReleaseLogicalAccountOwner"} {
		_, err = gateway.Forward(ctx, tradeService, method, codec.SerializationTypePB, nil)
		require.ErrorContains(t, err, "not allowed", method)
	}
	require.NoError(t, gateway.Close())
	require.Error(t, trade.CloseSession(ctx, space, id, instance, session, startedAt))
	require.NoError(t, os.WriteFile(filepath.Join(coord, "strategy-done"), []byte("passed"), 0600))
	t.Log("shared production client -> Directory -> private TLS native gateway -> Trade: account check, close session, CA, secret, space, ACL and Close passed")
}
