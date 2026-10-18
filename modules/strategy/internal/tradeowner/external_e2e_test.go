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
	owner := New(Config{Timeout: 3 * time.Second}, gateway)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	const space, instance, session = "space-gateway-e2e", "gateway-instance", "gateway-session"
	id := read("logical-id")
	// 没有可信 CA、CA 不匹配或签名密钥错误时，调用都必须失败。
	_, err = open("", credentials)
	require.ErrorContains(t, err, "private CA")
	untrusted, err := open(filepath.Join(coord, "unrelated-ca.pem"), credentials)
	require.NoError(t, err)
	require.Error(t, New(Config{}, untrusted).ClaimSession(ctx, space, id, instance, session))
	require.NoError(t, untrusted.Close())
	wrongCredentials := credentials
	wrongCredentials.Secret = "wrong-e2e-secret"
	wrong, err := open(filepath.Join(coord, "gateway-ca.pem"), wrongCredentials)
	require.NoError(t, err)
	require.Error(t, New(Config{}, wrong).ClaimSession(ctx, space, id, instance, session))
	require.NoError(t, wrong.Close())
	// 认领、校验、释放，以及空间与会话隔离。
	require.NoError(t, owner.ClaimSession(ctx, space, id, instance, session))
	require.NoError(t, owner.ValidateSession(ctx, space, id, instance, session))
	require.Error(t, owner.ValidateSession(ctx, space, id, instance, "wrong-session"))
	require.Error(t, owner.ClaimSession(ctx, "another-space", id, instance, session))
	// ACL：strategy 不能调用 Trade 的下单方法。
	_, err = gateway.Forward(ctx, tradeService, "SubmitOrder", codec.SerializationTypePB, nil)
	require.ErrorContains(t, err, "not allowed")
	require.NoError(t, owner.ValidateSession(ctx, space, id, instance, session))
	require.NoError(t, owner.ReleaseSession(ctx, space, id, instance, session))
	require.Error(t, owner.ValidateSession(ctx, space, id, instance, session))
	require.NoError(t, gateway.Close())
	require.Error(t, owner.ClaimSession(ctx, space, id, instance, session))
	require.NoError(t, os.WriteFile(filepath.Join(coord, "strategy-done"), []byte("passed"), 0600))
	t.Log("shared production client -> Directory -> private TLS native gateway -> Trade: ownership, CA, secret, space, session, ACL and Close passed")
}
