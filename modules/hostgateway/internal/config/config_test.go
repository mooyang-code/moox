package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/testcert"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
)

func TestSharedConfigurationAndPrivateIdentity(t *testing.T) {
	cfg := hostgatewayconfig.Default("storage", "control", "192.0.2.1", "fixture-key")
	cfg.TLS = testcert.Files(t, testcert.New(t, nil), "storage", nil)
	cfg.Control.KeyFile = filepath.Join(t.TempDir(), "caller.key")
	credential := testsnapshot.Credential(cfg.Control.Caller)
	require.NoError(t, os.WriteFile(cfg.Control.KeyFile, []byte(credential.Secret+"\n"), 0o600))
	encoded, err := hostgatewayconfig.Encode(cfg)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, encoded, 0o600))
	loaded, err := Load(path)
	require.NoError(t, err)
	material, signing, err := LoadIdentity(loaded)
	require.NoError(t, err)
	require.Equal(t, cfg.Host.ID, material.HostID())
	require.Equal(t, cfg.Control.Caller, signing.Caller)
	require.Equal(t, cfg.Control.KeyID, signing.KeyID)
	require.Equal(t, credential.Secret, signing.Secret)
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Chmod(cfg.Control.KeyFile, 0o644))
		_, _, err := LoadIdentity(cfg)
		require.Error(t, err)
		require.NoError(t, os.Chmod(cfg.Control.KeyFile, 0o600))
	}
	for _, invalid := range []string{"short", strings.Repeat("x", 4097), strings.Repeat("x", 32) + "\n" + strings.Repeat("y", 32)} {
		require.NoError(t, os.WriteFile(cfg.Control.KeyFile, []byte(invalid), 0o600))
		_, err := gatewayauth.ReadSigningSecret(cfg.Control.KeyFile)
		require.Error(t, err)
		require.NotContains(t, err.Error(), invalid)
	}
	for _, legacy := range []string{"node:\n  id: storage\n", string(encoded) + "auth:\n  credentials_file: old.json\n"} {
		require.NoError(t, os.WriteFile(path, []byte(legacy), 0o600))
		_, err := Load(path)
		require.Error(t, err)
	}
}
