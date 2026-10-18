package unitruntime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

func TestRuntimeHealthUsesSharedAuthenticationAndBindsResponseToRunningBinary(t *testing.T) {
	values := fixtureEnvironment()
	values["MOOX_HEALTH_AUTH_SECRET_KEY"] += "/allowed-in-secret"
	auth, err := healthz.NewAuthenticator(healthz.AuthConfig{
		Version: values["MOOX_HEALTH_AUTH_VERSION"], AccessKey: values["MOOX_HEALTH_AUTH_ACCESS_KEY"],
		SecretKey: values["MOOX_HEALTH_AUTH_SECRET_KEY"], ClockSkew: time.Minute, NonceTTL: time.Minute, MaxNonces: 10,
	})
	require.NoError(t, err)
	server := httptest.NewServer(auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, []string{"/healthz", "/readyz"}, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "binary_sha256": "running-binary", "boot_id": "current-boot"})
	})))
	defer server.Close()
	_, rawPort, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	state := runtimeState{catalog: servicecatalog.Catalog{Components: []servicecatalog.Component{{ID: "console-proxy", Health: servicecatalog.Health{Port: port}}}}}
	component := Component{ID: "console-proxy"}
	for _, readiness := range []bool{false, true} {
		ready, err := state.probe(context.Background(), component, pidRecord{BinarySHA256: "running-binary", BootID: "current-boot"}, values, readiness)
		require.NoError(t, err)
		require.True(t, ready)
	}
	_, err = state.probe(context.Background(), component, pidRecord{BinarySHA256: "different-binary"}, values, true)
	require.ErrorIs(t, err, errHealthIdentity)
	_, err = state.probe(context.Background(), component, pidRecord{BinarySHA256: "running-binary", BootID: "previous-boot"}, values, true)
	require.ErrorIs(t, err, errHealthIdentity)
	values["MOOX_HEALTH_AUTH_SECRET_KEY"] = "wrong-secret"
	_, err = state.probe(context.Background(), component, pidRecord{BinarySHA256: "running-binary", BootID: "current-boot"}, values, true)
	require.ErrorIs(t, err, errHealthIdentity)
}
