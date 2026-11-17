package ssh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// localClient 在本机执行 Run，用来跑 ReadPrivateFile 的远端脚本；其余操作不需要。
type localClient struct{}

func (localClient) Check(context.Context) error { return nil }
func (localClient) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, errors.New("not implemented")
}
func (localClient) Upload(context.Context, io.Reader, int64, string, fs.FileMode) error {
	return errors.New("not implemented")
}
func (localClient) Download(context.Context, string, io.Writer) (int64, error) {
	return 0, errors.New("not implemented")
}
func (localClient) Run(ctx context.Context, argv []string, stdin io.Reader) (Result, error) {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdin = stdin
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
		err = nil
	}
	return result, err
}
func (localClient) Close() error { return nil }

// 只读 0600 的常规文件：权限不对、软链、空文件、超过上限、不存在都拒绝。
func TestReadPrivateFileRejectsUnsafeRemoteFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.key")
	require.NoError(t, os.WriteFile(valid, []byte("secret\n"), 0o600))
	got, err := ReadPrivateFile(ctx, localClient{}, valid, 4096)
	require.NoError(t, err)
	require.Equal(t, "secret\n", string(got))

	unsafeMode := filepath.Join(dir, "mode.key")
	require.NoError(t, os.WriteFile(unsafeMode, []byte("secret"), 0o644))
	symlink := filepath.Join(dir, "link.key")
	require.NoError(t, os.Symlink(valid, symlink))
	empty := filepath.Join(dir, "empty.key")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	oversize := filepath.Join(dir, "oversize.key")
	require.NoError(t, os.WriteFile(oversize, bytes.Repeat([]byte("x"), 4097), 0o600))
	for _, path := range []string{filepath.Join(dir, "missing.key"), unsafeMode, symlink, empty, oversize, "relative.key"} {
		_, err := ReadPrivateFile(ctx, localClient{}, path, 4096)
		require.ErrorContains(t, err, "unavailable", path)
	}
	_, err = ReadPrivateFile(ctx, nil, valid, 4096)
	require.ErrorContains(t, err, "unavailable")
}
