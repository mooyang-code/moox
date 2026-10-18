package unitbootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/stretchr/testify/require"
)

func TestHostExportCheckpointsDistinguishAbsentFromInvalidPrivateFiles(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(directory, 0o700))
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	require.NoError(t, err)
	defer root.Close()
	_, err = readExportCheckpoint(root, "request.json", 4096)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "request.json"), []byte("{}\n"), 0o644))
	_, err = readExportCheckpoint(root, "request.json", 4096)
	require.Error(t, err)
	require.False(t, os.IsNotExist(err))
	require.NoError(t, os.Chmod(filepath.Join(directory, "request.json"), 0o600))
	raw, err := readExportCheckpoint(root, "request.json", 4096)
	require.NoError(t, err)
	require.Equal(t, "{}\n", string(raw))
}
