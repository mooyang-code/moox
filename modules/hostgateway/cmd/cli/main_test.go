package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testcert"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
)

func TestCLIValidatesNewIdentityAndPrintsPublicSnapshotOnly(t *testing.T) {
	cfg := hostgatewayconfig.Default("storage", "control", "192.0.2.1", "fixture-key")
	cfg.TLS = testcert.Files(t, testcert.New(t, nil), "storage", nil)
	cfg.Store.Path = filepath.Join(t.TempDir(), "cache")
	cfg.Control.KeyFile = filepath.Join(t.TempDir(), "caller.key")
	signing := testsnapshot.Credential(cfg.Control.Caller)
	require.NoError(t, os.WriteFile(cfg.Control.KeyFile, []byte(signing.Secret+"\n"), 0o600))
	encoded, err := hostgatewayconfig.Encode(cfg)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, encoded, 0o600))
	var output bytes.Buffer
	require.Zero(t, run([]string{"check-config", "--config", path}, &output))
	view, err := snapshot.Build("storage", testsnapshot.New(t, "storage", "storage-primary"))
	require.NoError(t, err)
	require.NoError(t, store.NewSnapshots(cfg.Store.Path, cfg.Host.ID).Save(view))
	output.Reset()
	require.Zero(t, run([]string{"routes", "--config", path}, &output))
	require.Contains(t, output.String(), "trpc.moox.storage.PrimaryStore")
	require.NotContains(t, output.String(), "verification_keys")
	require.NotContains(t, output.String(), signing.Secret)
	for _, key := range view.Proto().VerificationKeys {
		require.NotContains(t, output.String(), string(key.Secret))
	}
	require.NoError(t, os.WriteFile(store.NewSnapshots(cfg.Store.Path, cfg.Host.ID).Path(), []byte("corrupt"), 0o600))
	output.Reset()
	require.Equal(t, 1, run([]string{"routes", "--config", path}, &output))
	require.Equal(t, 1, run([]string{"check-config", "--config", filepath.Join(t.TempDir(), "missing")}, &output))
}

func TestCLIHealthSignsRequestsAndRejectsUnready(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "fixture")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "synthetic-health")
	auth, err := healthz.NewAuthenticator(healthz.AuthConfig{Version: "moox-health-v1", AccessKey: "fixture", SecretKey: "synthetic-health"})
	require.NoError(t, err)
	ready := httptest.NewServer(auth.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })))
	defer ready.Close()
	var output bytes.Buffer
	require.Zero(t, run([]string{"health", "--url", ready.URL + "/readyz"}, &output))
	unready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer unready.Close()
	require.Equal(t, 1, run([]string{"health", "--url", unready.URL + "/readyz"}, &output))
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "")
	require.Equal(t, 1, run([]string{"health", "--url", ready.URL + "/readyz"}, &output))
}
