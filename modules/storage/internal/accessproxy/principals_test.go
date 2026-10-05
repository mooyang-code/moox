package accessproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writePrincipalsFixture(t *testing.T, secretMode os.FileMode, preset string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"collector.key", "factor-engine.key", "factor.key"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name+"-secret\n"), secretMode))
	}
	path := filepath.Join(dir, "principals.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`version: 1
principals:
  - name: collector
    methods: collector
    inbound: {key_id: collector, caller: collector, secret_file: collector.key}
    upstream: {key_id: collector, caller: collector, secret_file: collector.key}
  - name: factor-engine
    methods: `+preset+`
    inbound: {key_id: factor-engine, caller: factor-engine, secret_file: factor-engine.key}
    upstream: {key_id: factor, caller: factor, secret_file: factor.key}
`), 0o600))
	return path
}

func TestLoadPrincipals(t *testing.T) {
	principals, err := LoadPrincipals(writePrincipalsFixture(t, 0o600, "factor-engine"))

	require.NoError(t, err)
	require.Len(t, principals, 2)
	engine := principals[1]
	require.Equal(t, "factor-engine", engine.Inbound.Caller)
	require.Equal(t, "factor", engine.Upstream.Caller)
	require.Equal(t, "factor-engine.key-secret", engine.Inbound.Secret)
	require.True(t, engine.allows(PrimaryStoreName, "WriteFactorRows"))
	require.False(t, engine.allows(PrimaryStoreName, "DeleteDatasetRows"))
}

func TestPrincipalsFileRequires0600Secrets(t *testing.T) {
	_, err := LoadPrincipals(writePrincipalsFixture(t, 0o644, "factor-engine"))
	require.ErrorContains(t, err, "0600")
}

func TestPrincipalsRejectUnknownPreset(t *testing.T) {
	_, err := LoadPrincipals(writePrincipalsFixture(t, 0o600, "everything"))
	require.ErrorContains(t, err, "unknown method preset")
}
