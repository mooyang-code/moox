package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const example = `gateway_client:
  caller: access
  key_id: assigned-internal-access-75
  key_file: ../../secrets/caller-access.key
verification_file: ../../secrets/access/access-verification.json
nonce_path: ../../data/access/nonces.db
`

func TestConfigUsesCanonicalLayoutAndRejectsLegacyFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "access", "config", "app.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(example), 0o644))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, path, cfg.SourcePath)
	require.Equal(t, filepath.Join(root, "secrets", "access", "access-verification.json"), cfg.VerificationFile)
	require.Equal(t, filepath.Join(root, "data", "access", "nonces.db"), cfg.NoncePath)
	for _, invalid := range []string{
		example + "upstream_target: do-not-echo-this-secret\n",
		example + "---\n" + example,
		strings.Replace(example, "caller: access", "caller: collector", 1),
		strings.Replace(example, "nonce_path: ../../data/access/nonces.db", "nonce_path: ''", 1),
		strings.Repeat("a", (1<<20)+1),
	} {
		require.NoError(t, os.WriteFile(path, []byte(invalid), 0o644))
		_, err := Load(path)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "do-not-echo-this-secret")
	}
}
