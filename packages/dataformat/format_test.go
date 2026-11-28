package dataformat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildWriteReadAndDiff(t *testing.T) {
	rec1, err := Build("strategy", map[string]string{
		PartSQLiteSchema: "sha256:aaa",
		PartLifecycle:    "1",
	})
	require.NoError(t, err)
	rec2, err := Build("strategy", map[string]string{
		PartLifecycle:    "1",
		PartSQLiteSchema: "sha256:aaa",
	})
	require.NoError(t, err)
	require.Equal(t, rec1.Format, rec2.Format, "组成部分顺序不影响摘要")

	dir := t.TempDir()
	require.NoError(t, WriteDir(dir, rec1))

	loaded, err := ReadFile(filepath.Join(dir, FileName))
	require.NoError(t, err)
	require.Equal(t, rec1, loaded)

	// 篡改摘要后解析必须失败。
	corrupted := filepath.Join(dir, "bad")
	require.NoError(t, os.WriteFile(corrupted, []byte(`{"component":"strategy","format":"sha256:deadbeef","parts":{"lifecycle":"1"}}`), 0o600))
	_, err = ReadFile(corrupted)
	require.Error(t, err)

	rec3, err := Build("strategy", map[string]string{
		PartSQLiteSchema: "sha256:bbb",
		PartLifecycle:    "2",
	})
	require.NoError(t, err)
	require.NotEqual(t, rec1.Format, rec3.Format)
	require.Equal(t, []string{PartLifecycle, PartSQLiteSchema}, DiffParts(rec3, rec1))
}
