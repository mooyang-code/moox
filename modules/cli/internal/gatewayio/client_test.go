package gatewayio

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/stretchr/testify/require"
)

// 操作电脑的身份就是 ~/.config/moox/moox-cli.key 这一把私钥：没有时提示怎么得到它；权限放宽、是符号链接或内容不对都拒绝，
// 不回退到别的身份。
func TestOperatorKeyIsRequiredAndMustBePrivate(t *testing.T) {
	directory := t.TempDir()
	open := func() error {
		_, err := open(context.Background(), nil, "control", directory, setupssh.Options{})
		return err
	}
	require.ErrorContains(t, open(), "setup operator --register")
	path := filepath.Join(directory, OperatorKeyFile)
	content := []byte(gatewayauth.EncodeSigningKey(authtest.Key("operator")) + "\n")
	require.NoError(t, os.WriteFile(path, content, 0o644))
	require.ErrorContains(t, open(), "load operator signing key")
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.WriteFile(path, []byte("caller: moox-cli\nkey_id: old\n"), 0o600))
	require.ErrorContains(t, open(), "load operator signing key")
	require.NoError(t, os.Remove(path))
	target := filepath.Join(t.TempDir(), "elsewhere.key")
	require.NoError(t, os.WriteFile(target, content, 0o600))
	require.NoError(t, os.Symlink(target, path))
	require.ErrorContains(t, open(), "load operator signing key")
	// 私钥没问题时走到取服务目录这一步（这里没有可连的控制机，所以在那里失败），说明身份已经读出来了。
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, content, 0o600))
	err := open()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "signing key")
}
