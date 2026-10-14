package unitruntime

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRotateRuntimeLogKeepsBoundedBackups(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	write := func(name, content string) {
		require.NoError(t, os.WriteFile(dir+"/"+name, []byte(content), 0o600))
	}
	read := func(name string) string {
		raw, err := os.ReadFile(dir + "/" + name)
		require.NoError(t, err)
		return string(raw)
	}

	// 不存在、不超限：什么都不做。
	require.NoError(t, rotateRuntimeLog(root, "svc.log", 10, 3))
	write("svc.log", "small")
	require.NoError(t, rotateRuntimeLog(root, "svc.log", 10, 3))
	require.Equal(t, "small", read("svc.log"))

	// 连续超限：依次后移，最多保留 3 份，最旧的被丢弃。
	for i := 1; i <= 5; i++ {
		write("svc.log", strings.Repeat(string(rune('a'+i-1)), 20))
		require.NoError(t, rotateRuntimeLog(root, "svc.log", 10, 3))
		_, err := os.Stat(dir + "/svc.log")
		require.True(t, os.IsNotExist(err), "超限的日志被移走，由启动时重新创建")
	}
	require.Equal(t, strings.Repeat("e", 20), read("svc.log.1"))
	require.Equal(t, strings.Repeat("d", 20), read("svc.log.2"))
	require.Equal(t, strings.Repeat("c", 20), read("svc.log.3"))
	_, err = os.Stat(dir + "/svc.log.4")
	require.True(t, os.IsNotExist(err), "超出备份数量的旧日志被删除")
}
