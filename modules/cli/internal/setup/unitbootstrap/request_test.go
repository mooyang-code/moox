package unitbootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/stretchr/testify/require"
)

func TestBootstrapRequestRejectsUnsafeAndAmbiguousPrivateInput(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(root, 0o700))
	filename := filepath.Join(root, "request.json")
	request := Request{Version: 1, Control: Unit{Environment: map[string]map[string]string{"admin": {"MOOX_TEST_SECRET": "private-bootstrap-test-value"}}}}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filename, append(raw, '\n'), 0o600))
	read, err := ReadRequest(filename)
	require.NoError(t, err)
	require.Equal(t, request, read)
	require.NotContains(t, fmt.Sprintf("%#v", request), "private-bootstrap-test-value")
	for _, bad := range [][]byte{raw, append(append([]byte(nil), raw...), []byte("\n{}\n")...), []byte("{\"version\":1,\"version\":2,\"secret\":\"private-bootstrap-test-value\"}\n")} {
		require.NoError(t, os.WriteFile(filename, bad, 0o600))
		_, err := ReadRequest(filename)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-bootstrap-test-value")
	}
	require.NoError(t, os.WriteFile(filename, append(raw, '\n'), 0o600))
	require.NoError(t, os.Chmod(filename, 0o644))
	_, err = ReadRequest(filename)
	require.Error(t, err)
	require.NoError(t, os.Chmod(filename, 0o600))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(filename, link))
	_, err = ReadRequest(link)
	require.Error(t, err)
}

func TestBootstrapPublicOutputBoundAndJournalIdentities(t *testing.T) {
	var output boundedOutput
	_, err := output.Write(make([]byte, 128<<10))
	require.NoError(t, err)
	_, err = output.Write([]byte("x"))
	require.Error(t, err)
	require.False(t, validDigest("short"))
	require.False(t, validDigest("sha256:"+string(make([]byte, 64))))
	require.False(t, validAttempt("../escape"))
	require.False(t, validAttempt(".hidden"))
	require.Equal(t, []string{"web-host"}, remaining([]string{"admin", "eventbus", "web-host"}, "admin", "eventbus"))
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(root, 0o700))
	directory, err := fsutil.OpenPhysicalRoot(root, true)
	require.NoError(t, err)
	defer directory.Close()
	require.NoError(t, fsutil.WritePrivate(directory, "secrets/runtime-admin.json", []byte("{}\n"), false))
	require.NoError(t, fsutil.WritePrivate(directory, "admin/config/app.yaml", []byte("database:\n  path: ./data/admin.db\n"), false))
	require.Error(t, validateAdminState(root), "missing existing data must never become a fresh initialization")
	require.NoError(t, fsutil.WritePrivate(directory, "admin/data/admin.db", []byte("closed synthetic database"), false))
	require.NoError(t, validateAdminState(root))
	require.NoError(t, fsutil.WritePrivate(directory, "secrets/runtime-admin.json", []byte("{\"MOOX_ADMIN_DB_PATH\":\"/outside/admin.db\"}\n"), true))
	require.Error(t, validateAdminState(root), "outside database must never be silently omitted from the snapshot")
}
