package gatewayio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperatorIdentityRejectsUnsafeAndObsoleteConfiguration(t *testing.T) {
	base := "caller: moox-cli\nkey_id: admin-generated-key\nkey_file: caller-moox-cli.key\n"
	for _, raw := range []string{strings.Replace(base, "moox-cli", "collector", 1), strings.Replace(base, "admin-generated-key", "", 1), strings.Replace(base, "admin-generated-key", "invalid/key", 1), base + "target: ip://public.example:11003\n", base + "caller: moox-cli\n", base + "---\n{}\n"} {
		path := filepath.Join(t.TempDir(), "identity.yaml")
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
		_, err := loadIdentity(path)
		require.Error(t, err)
	}
	path := filepath.Join(t.TempDir(), "identity.yaml")
	require.NoError(t, os.WriteFile(path, []byte(base), 0o644))
	_, err := loadIdentity(path)
	require.Error(t, err)
	require.NoError(t, os.Chmod(path, 0o600))
	link := path + ".link"
	require.NoError(t, os.Symlink(path, link))
	_, err = loadIdentity(link)
	require.Error(t, err)
}
