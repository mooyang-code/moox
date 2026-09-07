//go:build e2e_external

package bootstrap

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
)

func TestExternalStrategyGatewayOwnerClient(t *testing.T) {
	coord := os.Getenv("MOOX_GATEWAY_OWNER_E2E_COORD")
	require.NotEmpty(t, coord)
	endpoint, err := os.ReadFile(filepath.Join(coord, "gateway-ready"))
	require.NoError(t, err)
	u, err := url.Parse(string(endpoint))
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", u.Hostname())
	require.Equal(t, "https", u.Scheme)
	id, err := os.ReadFile(filepath.Join(coord, "logical-id"))
	require.NoError(t, err)
	cfg := TradeConfig{GatewayURL: string(endpoint), TargetNode: "gateway-owner-e2e", CAFile: filepath.Join(coord, "gateway-ca.pem"), Timeout: 3 * time.Second}
	owner := newLogicalAccountOwnerClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	const space, instance, session = "space-gateway-e2e", "gateway-instance", "gateway-session"
	require.NoError(t, owner.Validate(ctx, space, string(id)))
	for name, caFile := range map[string]string{"missing CA": "", "unrelated CA": filepath.Join(coord, "unrelated-ca.pem")} {
		t.Run(name, func(t *testing.T) {
			untrusted := cfg
			untrusted.CAFile = caFile
			require.ErrorContains(t, newLogicalAccountOwnerClient(untrusted).Validate(ctx, space, string(id)), "x509:")
		})
	}
	require.NoError(t, owner.ClaimSession(ctx, space, string(id), instance, session))
	require.NoError(t, owner.ValidateSession(ctx, space, string(id), instance, session))
	require.Error(t, owner.ValidateSession(ctx, space, string(id), instance, "wrong-session"))
	require.Error(t, owner.Validate(ctx, "another-space", string(id)))
	require.Error(t, owner.ClaimSession(ctx, "another-space", string(id), instance, session))
	wrongNode := cfg
	wrongNode.TargetNode = "wrong-node"
	require.ErrorContains(t, newLogicalAccountOwnerClient(wrongNode).Validate(ctx, space, string(id)), "HTTP 401")
	t.Run("wrong secret", func(t *testing.T) {
		t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "wrong-e2e-secret")
		require.ErrorContains(t, newLogicalAccountOwnerClient(cfg).Validate(ctx, space, string(id)), "HTTP 401")
	})
	path := "/api/service/trade_owner/SubmitOrder"
	body := []byte(`{}`)
	headers, err := gatewayauth.Sign(gatewayauth.CredentialsFromEnv(), gatewayauth.Request{Method: http.MethodPost, Path: path, TargetNode: cfg.TargetNode, Body: body}, time.Now())
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.GatewayURL+path, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header = headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Space-Id", space)
	httpClient, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: 3 * time.Second, CAFile: cfg.CAFile})
	require.NoError(t, err)
	defer httpClient.CloseIdleConnections()
	rsp, err := httpClient.Do(req)
	require.NoError(t, err)
	rsp.Body.Close()
	require.Equal(t, http.StatusNotFound, rsp.StatusCode, "owner-only route must reject SubmitOrder")
	require.NoError(t, owner.ValidateSession(ctx, space, string(id), instance, session))
	require.NoError(t, owner.ReleaseSession(ctx, space, string(id), instance, session))
	require.Error(t, owner.ValidateSession(ctx, space, string(id), instance, session))
	require.NoError(t, os.WriteFile(filepath.Join(coord, "strategy-done"), []byte("passed"), 0600))
	t.Log("production client -> trusted TLS Gateway -> Trade: Validate/ClaimSession/ValidateSession/ReleaseSession; missing/unrelated CA, wrong node/secret/space and SubmitOrder rejected")
}
