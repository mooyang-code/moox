package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func buildMooxCLI(t *testing.T) string {
	t.Helper()
	if binary := prebuiltE2EBinary(t, "MOOX_CLI_E2E_BINARY"); binary != "" {
		return binary
	}
	binary := filepath.Join(t.TempDir(), "moox-cli")
	command := exec.Command("go", "build", "-o", binary, "../cmd/moox-cli")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "build moox-cli: %s", output)
	return binary
}

// 跨平台验证时执行本机预先编译好的纯 Go 产物，不在 Linux 执行机上调用编译器。
func prebuiltE2EBinary(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	if path != "" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.True(t, info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0)
	}
	return path
}
