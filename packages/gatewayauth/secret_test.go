package gatewayauth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadSigningSecretRejectsUnsafeFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "caller.key")
	secret := strings.Repeat("k", 32)
	require.NoError(t, os.WriteFile(path, []byte(secret+"\n"), 0o600))
	got, err := ReadSigningSecret(path)
	require.NoError(t, err)
	require.Equal(t, secret, got)
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Chmod(path, 0o640))
		_, err := ReadSigningSecret(path)
		require.Error(t, err)
		require.NoError(t, os.Chmod(path, 0o600))
		link := path + ".link"
		require.NoError(t, os.Symlink(path, link))
		_, err = ReadSigningSecret(link)
		require.Error(t, err)
	}
	_, err = ReadSigningSecret(filepath.Dir(path))
	require.Error(t, err)
	for _, raw := range []string{"short", strings.Repeat("k", 4097), secret + "\n" + secret} {
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
		_, err := ReadSigningSecret(path)
		require.Error(t, err)
		require.NotContains(t, err.Error(), raw)
	}
}
