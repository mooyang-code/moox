package unitdeploy

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/stretchr/testify/require"
)

// releaseELF 是能通过打包校验的最小 Linux ELF 可执行文件。
func releaseELF(arch string) []byte {
	raw := make([]byte, 120)
	copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 2)
	machine := uint16(62)
	if arch == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(raw[18:], machine)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[24:], 0x401000)
	binary.LittleEndian.PutUint64(raw[32:], 64)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 1)
	binary.LittleEndian.PutUint32(raw[64:], 1)
	binary.LittleEndian.PutUint32(raw[68:], 5)
	binary.LittleEndian.PutUint64(raw[80:], 0x400000)
	binary.LittleEndian.PutUint64(raw[96:], uint64(len(raw)))
	binary.LittleEndian.PutUint64(raw[104:], uint64(len(raw)))
	return raw
}

// physicalTemp 返回没有符号链接的私有临时目录（macOS 的 /var 是指向 /private/var 的链接，私有状态目录要求物理路径）。
func physicalTemp(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(directory, 0o700))
	return directory
}

func releaseFixture(t *testing.T, commit string) ReleaseOptions {
	t.Helper()
	bin := t.TempDir()
	for _, profile := range releaseProfiles {
		names, err := unitpackage.Binaries(profile)
		require.NoError(t, err)
		for _, name := range names {
			require.NoError(t, os.WriteFile(filepath.Join(bin, name), releaseELF("amd64"), 0o755))
		}
	}
	repository, err := filepath.Abs("../../../../..")
	require.NoError(t, err)
	return ReleaseOptions{
		RepositoryRoot: repository, StateDirectory: filepath.Join(physicalTemp(t), "state"), ReleaseID: "r1", Architecture: "amd64", BinaryDirectory: bin,
		commit: func(context.Context, string) (string, error) { return commit, nil },
	}
}

// 构建版本制品：包含五个纯 Go 单元与运行助手；同一 ID、同一提交复用；提交不同拒绝；篡改软件包后读取失败。
func TestReleaseBuildReuseAndVerification(t *testing.T) {
	options := releaseFixture(t, "c0ffee")
	built, err := BuildRelease(t.Context(), options)
	require.NoError(t, err)
	require.Len(t, built.Packages, len(releaseProfiles))
	require.Equal(t, "c0ffee", built.SourceCommit)

	again, err := BuildRelease(t.Context(), options)
	require.NoError(t, err)
	require.Equal(t, built.Packages["control"].SHA256, again.Packages["control"].SHA256)
	require.Equal(t, built.Helper, again.Helper, "同一版本不重新构建")

	other := options
	other.commit = func(context.Context, string) (string, error) { return "deadbeef", nil }
	_, err = BuildRelease(t.Context(), other)
	require.ErrorContains(t, err, "not the current commit")

	read, err := ReadRelease(t.Context(), options.StateDirectory, "r1", "amd64")
	require.NoError(t, err)
	require.Equal(t, built.Packages["host"].SHA256, read.Packages["host"].SHA256)
	_, err = ReadRelease(t.Context(), options.StateDirectory, "r1", "arm64")
	require.Error(t, err, "架构不符")
	_, err = ReadRelease(t.Context(), options.StateDirectory, "r2", "amd64")
	require.ErrorContains(t, err, "does not exist")

	require.NoError(t, os.WriteFile(built.Packages["trade"].Archive, []byte("tampered"), 0o600))
	_, err = ReadRelease(t.Context(), options.StateDirectory, "r1", "amd64")
	require.ErrorContains(t, err, "trade artifact failed independent verification")
}

func TestReleaseRejectsInvalidInput(t *testing.T) {
	options := releaseFixture(t, "c0ffee")
	for _, id := range []string{"", "R1", "a/b", "-x", "0123456789012345678901234567890123456789x"} {
		bad := options
		bad.ReleaseID = id
		_, err := BuildRelease(t.Context(), bad)
		require.Error(t, err, id)
	}
	bad := options
	bad.Architecture = "386"
	_, err := BuildRelease(t.Context(), bad)
	require.ErrorContains(t, err, "architecture")
}

// control 请求绑定：首次完整 control 与各升级版本各自一条记录；同一版本换请求拒绝，不同版本互不影响。
func TestControlRequestBindingPerRelease(t *testing.T) {
	directory := filepath.Join(physicalTemp(t), "state")
	root, err := privateRoot(directory)
	require.NoError(t, err)
	defer root.Close()
	require.NoError(t, bindControlRequest(root, "", "sha256:first"))
	require.Error(t, bindControlRequest(root, "", "sha256:other"), "首次绑定不能被替换")
	require.NoError(t, bindControlRequest(root, "r1", "sha256:upgrade-1"))
	require.NoError(t, bindControlRequest(root, "r1", "sha256:upgrade-1"), "同一版本重试")
	require.Error(t, bindControlRequest(root, "r1", "sha256:changed"), "同一版本换请求")
	require.NoError(t, bindControlRequest(root, "r2", "sha256:upgrade-2"))
	require.Error(t, bindControlRequest(root, "../x", "sha256:x"))
	raw, err := fsutil.ReadPrivate(root, "control-operations/r1.json", 4096)
	require.NoError(t, err)
	require.Contains(t, string(raw), "upgrade-1")
}
