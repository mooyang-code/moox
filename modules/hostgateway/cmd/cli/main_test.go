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
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
)

func TestCLIValidatesNewIdentityAndPrintsPublicSnapshotOnly(t *testing.T) {
	cfg := hostgatewayconfig.Default("storage", "control", "192.0.2.1")
	cfg.TLS = testcert.Files(t, testcert.New(t, nil), "storage", nil)
	cfg.Store.Path = filepath.Join(t.TempDir(), "cache")
	cfg.Control.KeyFile = filepath.Join(t.TempDir(), "host.key")
	signing := gatewayauth.EncodeSigningKey(testsnapshot.HostKey("storage"))
	require.NoError(t, os.WriteFile(cfg.Control.KeyFile, []byte(signing+"\n"), 0o600))
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
	require.NotContains(t, output.String(), signing)
	for _, key := range view.Proto().VerificationKeys {
		require.NotContains(t, output.String(), string(key.PublicKey))
	}
	require.NoError(t, os.WriteFile(store.NewSnapshots(cfg.Store.Path, cfg.Host.ID).Path(), []byte("corrupt"), 0o600))
	output.Reset()
	require.Equal(t, 1, run([]string{"routes", "--config", path}, &output))
	require.Equal(t, 1, run([]string{"check-config", "--config", filepath.Join(t.TempDir(), "missing")}, &output))
	// 部署用它核对目标机上的私钥和 Admin 登记的公钥是否一致：只打印公钥。
	output.Reset()
	require.Zero(t, run([]string{"sign-public-key", "--key-file", cfg.Control.KeyFile}, &output))
	require.Equal(t, gatewayauth.EncodePublicKey(testsnapshot.Credential("host-gateway", "storage").PublicKey())+"\n", output.String())
	require.Equal(t, 1, run([]string{"sign-public-key", "--key-file", filepath.Join(t.TempDir(), "missing")}, &output))
}

func TestCLIHealthRejectsUnready(t *testing.T) {
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Values("Authorization"))
		w.WriteHeader(http.StatusOK)
	}))
	defer ready.Close()
	var output bytes.Buffer
	require.Zero(t, run([]string{"health", "--url", ready.URL + "/readyz"}, &output))
	unready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer unready.Close()
	require.Equal(t, 1, run([]string{"health", "--url", unready.URL + "/readyz"}, &output))
}
