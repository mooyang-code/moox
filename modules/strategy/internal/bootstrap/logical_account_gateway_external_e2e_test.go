//go:build e2e_external

package bootstrap

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
	owner := newLogicalAccountOwnerClient(TradeConfig{Timeout: 3 * time.Second}, gateway)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	const space, instance, session = "space-gateway-e2e", "gateway-instance", "gateway-session"
	id := read("logical-id")
	require.NoError(t, owner.Validate(ctx, space, id))
	_, err = open("", credentials)
	require.ErrorContains(t, err, "private CA")
	untrusted, err := open(filepath.Join(coord, "unrelated-ca.pem"), credentials)
	require.NoError(t, err)
	require.Error(t, newLogicalAccountOwnerClient(TradeConfig{}, untrusted).Validate(ctx, space, id))
	require.NoError(t, untrusted.Close())
	wrongCredentials := credentials
	wrongCredentials.Secret = "wrong-e2e-secret"
	wrong, err := open(filepath.Join(coord, "gateway-ca.pem"), wrongCredentials)
	require.NoError(t, err)
	require.ErrorContains(t, newLogicalAccountOwnerClient(TradeConfig{}, wrong).Validate(ctx, space, id), "authentication failed")
	require.NoError(t, wrong.Close())
	require.NoError(t, owner.ClaimSession(ctx, space, id, instance, session))
	require.NoError(t, owner.ValidateSession(ctx, space, id, instance, session))
	require.Error(t, owner.ValidateSession(ctx, space, id, instance, "wrong-session"))
	require.Error(t, owner.Validate(ctx, "another-space", id))
	require.Error(t, owner.ClaimSession(ctx, "another-space", id, instance, session))
	_, err = gateway.Forward(ctx, tradeConsolePath, "SubmitOrder", codec.SerializationTypePB, nil)
	require.ErrorContains(t, err, "not allowed")
	require.NoError(t, owner.ValidateSession(ctx, space, id, instance, session))
	require.NoError(t, owner.ReleaseSession(ctx, space, id, instance, session))
	require.Error(t, owner.ValidateSession(ctx, space, id, instance, session))
	require.NoError(t, gateway.Close())
	require.Error(t, owner.Validate(ctx, space, id))
	require.NoError(t, os.WriteFile(filepath.Join(coord, "strategy-done"), []byte("passed"), 0600))
	t.Log("shared production client -> Directory -> private TLS native gateway -> Trade: ownership, CA, secret, space, session, ACL and Close passed")
}
