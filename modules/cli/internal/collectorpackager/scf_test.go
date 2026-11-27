package collectorpackager

import (
	"archive/zip"
	"debug/elf"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSCFPackageRequiresBinaryAndOutPath(t *testing.T) {
	_, err := BuildSCFPackage(BuildSCFPackageOptions{OutPath: "out.zip"})
	require.ErrorContains(t, err, "binary path is required")
	_, err = BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: "main"})
	require.ErrorContains(t, err, "output path is required")
}

// 市场数据文件与 binance.yaml 全部内嵌在二进制中，SCF 代码包只包含单一 main 可执行文件。
func TestBuildSCFPackageProducesSingleMainBinary(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	out := filepath.Join(tmp, "package.zip")

	result, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, OutPath: out})
	require.NoError(t, err)
	assert.Equal(t, []string{"main"}, result.Entries)
	require.NoError(t, ValidateSCFPackageZip(out))

	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// 打包不读环境变量：本机环境里的密钥不会进代码包。
func TestBuildSCFPackageDoesNotEmbedEnvironmentSecrets(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	out := filepath.Join(tmp, "package.zip")
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	t.Setenv("MOOX_EVENTBUS_NATS_PASSWORD", "test-eventbus-password")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-gateway-secret")

	_, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, OutPath: out})
	require.NoError(t, err)
	require.NoError(t, ValidateSCFPackageZip(out))

	reader, err := zip.OpenReader(out)
	require.NoError(t, err)
	defer reader.Close()
	require.Len(t, reader.File, 1)
	rc, err := reader.File[0].Open()
	require.NoError(t, err)
	content, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.NotContains(t, string(content), "test-eventbus-password")
	assert.NotContains(t, string(content), "test-gateway-secret")
}

func TestBuildSCFPackageRejectsSymlinkedBinary(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "real-main")
	linked := filepath.Join(tmp, "main")
	out := filepath.Join(tmp, "package.zip")
	require.NoError(t, os.WriteFile(target, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	require.NoError(t, os.Symlink(target, linked))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: linked, OutPath: out})
	require.ErrorContains(t, err, "symlink")
	_, statErr := os.Stat(out)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestBuildSCFPackageDoesNotLeaveInvalidOutput(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	require.NoError(t, os.WriteFile(binary, []byte("not-an-elf"), 0o755))
	out := filepath.Join(tmp, "invalid.zip")

	_, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, OutPath: out})
	require.ErrorContains(t, err, "main")
	_, statErr := os.Stat(out)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "failed validation must not leave a publishable ZIP")
}

// 代码包只允许单一 main 二进制：任何额外 YAML 配置、证书或目录外载荷都会被拒绝。
func TestValidateSCFPackageZipRejectsUnexpectedPayload(t *testing.T) {
	require.NoError(t, ValidateSCFPackageZip(writeTestPackage(t, map[string][]byte{"main": testfixture.LinuxExecutable(elf.EM_X86_64)})))

	for _, extra := range []string{"sources/market/binance.yaml", "certs/eventbus-ca.pem", "trpc_go.yaml"} {
		path := writeTestPackage(t, map[string][]byte{
			"main": testfixture.LinuxExecutable(elf.EM_X86_64),
			extra:  []byte("payload: true\n"),
		})
		require.ErrorContains(t, ValidateSCFPackageZip(path), "unexpected scf package payload", extra)
	}
}

func TestValidateSCFPackageZipRejectsDuplicatePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duplicate.zip")
	file, err := os.Create(path)
	require.NoError(t, err)
	zw := zip.NewWriter(file)
	for range 2 {
		require.NoError(t, addZipBytes(zw, testfixture.LinuxExecutable(elf.EM_X86_64), "main", 0o755))
	}
	require.NoError(t, zw.Close())
	require.NoError(t, file.Close())
	require.ErrorContains(t, ValidateSCFPackageZip(path), "duplicate")
}

func TestValidateSCFPackageZipRequiresExecutableLinuxMain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
		mode    os.FileMode
	}{
		{"text", []byte("binary"), 0o755},
		{"empty", nil, 0o755},
		{"not executable", testfixture.LinuxExecutable(elf.EM_X86_64), 0o644},
		{"wrong architecture", testfixture.LinuxExecutable(elf.EM_386), 0o755},
		{"directory", nil, os.ModeDir | 0o755},
		{"private PEM", append(testfixture.LinuxExecutable(elf.EM_X86_64), []byte("\n-----BEGIN PRIVATE KEY-----\nprivate-test-secret\n-----END PRIVATE KEY-----\n")...), 0o755},
		{"credential assignment", append(testfixture.LinuxExecutable(elf.EM_X86_64), []byte("\nMOOX_EVENTBUS_NATS_PASSWORD=private-test-secret\n")...), 0o755},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "package.zip")
			file, err := os.Create(path)
			require.NoError(t, err)
			zw := zip.NewWriter(file)
			require.NoError(t, addZipBytes(zw, tc.content, "main", tc.mode))
			require.NoError(t, zw.Close())
			require.NoError(t, file.Close())
			err = ValidateSCFPackageZip(path)
			require.ErrorContains(t, err, "main")
			require.NotContains(t, err.Error(), "private-test-secret")
		})
	}
	for _, machine := range []elf.Machine{elf.EM_X86_64, elf.EM_AARCH64} {
		t.Run(machine.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "package.zip")
			file, err := os.Create(path)
			require.NoError(t, err)
			zw := zip.NewWriter(file)
			require.NoError(t, addZipBytes(zw, testfixture.LinuxExecutable(machine), "main", 0o755))
			require.NoError(t, zw.Close())
			require.NoError(t, file.Close())
			require.NoError(t, ValidateSCFPackageZip(path))
		})
	}
}

func TestValidateSCFPackageZipRequiresMain(t *testing.T) {
	require.ErrorContains(t, ValidateSCFPackageZip(writeTestPackage(t, map[string][]byte{})), "main")
}

func writeTestPackage(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "package.zip")
	file, err := os.Create(path)
	require.NoError(t, err)
	zw := zip.NewWriter(file)
	for name, content := range files {
		mode := os.FileMode(0o644)
		if name == "main" {
			mode = 0o755
		}
		require.NoError(t, addZipBytes(zw, content, name, mode))
	}
	require.NoError(t, zw.Close())
	require.NoError(t, file.Close())
	return path
}
