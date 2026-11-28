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

func TestVerifyOrWriteFileBehavior(t *testing.T) {
	v1, err := Build("storage-view", map[string]string{PartStorageViewIndex: "1"})
	require.NoError(t, err)
	v2, err := Build("storage-view", map[string]string{PartStorageViewIndex: "2"})
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, FileName)

	// 1. 已有数据但没有格式文件：照常写入。
	require.NoError(t, VerifyOrWriteFile(path, v1, true))
	got, err := ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, v1, got)

	// 2. 已有数据且格式相符：直接通过。
	require.NoError(t, VerifyOrWriteFile(path, v1, true))

	// 3. 已有数据且格式不符：拒绝启动并提示 setup install --wipe，且不覆盖旧格式文件。
	err = VerifyOrWriteFile(path, v2, true)
	require.ErrorIs(t, err, ErrIncompatibleFormat)
	require.ErrorContains(t, err, PartStorageViewIndex)
	require.ErrorContains(t, err, "setup install --wipe")
	still, err := ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, v1, still, "拒绝启动时不应覆盖磁盘上的格式文件")

	// 4. 尚无数据时即使存在旧格式文件也照常覆盖写入。
	require.NoError(t, VerifyOrWriteFile(path, v2, false))
	updated, err := ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, v2, updated)

	// 5. 已有数据且格式文件损坏：同样拒绝启动。
	require.NoError(t, os.WriteFile(path, []byte("{corrupt"), 0o600))
	err = VerifyOrWriteFile(path, v2, true)
	require.ErrorIs(t, err, ErrIncompatibleFormat)
	require.ErrorContains(t, err, "setup install --wipe")

	// 6. DirHasEntries 忽略目录里孤立的 data-format 文件。
	emptyDir := t.TempDir()
	require.False(t, DirHasEntries(emptyDir))
	require.NoError(t, WriteDir(emptyDir, v2))
	require.False(t, DirHasEntries(emptyDir))
	require.NoError(t, os.WriteFile(filepath.Join(emptyDir, "index.db"), []byte("x"), 0o600))
	require.True(t, DirHasEntries(emptyDir))
}
