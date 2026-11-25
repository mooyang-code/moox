package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 只读 *.yaml，按文件名排序，文件名只带基本名。
func TestReadFactorCatalogDir(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"b.yaml": "- b", "a.yaml": "- a", "note.md": "x"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	files, err := readFactorCatalogDir(dir)
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.Equal(t, "a.yaml", files[0].GetName())
	require.Equal(t, "- a", files[0].GetContent())
	require.Equal(t, "b.yaml", files[1].GetName())

	_, err = readFactorCatalogDir(t.TempDir())
	require.ErrorContains(t, err, "没有 *.yaml")
}
