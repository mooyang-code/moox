package identitybundle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
)

// moox-cli 的私钥只在操作电脑上生成：已有就原样复用（重跑安装不换密钥），没有才生成；文件 0600。
func TestEnsureSigningKeyFileCreatesOnceAndReuses(t *testing.T) {
	directory := filepath.Join(privateParent(t), "moox")
	path := filepath.Join(directory, OperatorKeyFile)
	first, created, err := EnsureSigningKeyFile(path)
	require.NoError(t, err)
	require.True(t, created)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	second, created, err := EnsureSigningKeyFile(path)
	require.NoError(t, err)
	require.False(t, created)
	require.True(t, first.Equal(second))
	loaded, err := ReadOperatorKey(directory)
	require.NoError(t, err)
	require.Equal(t, PublicKey(first), PublicKey(loaded))
	_, err = gatewayauth.ParsePublicKey(PublicKey(first))
	require.NoError(t, err)
	// 文件坏了或权限放宽了：不悄悄换一把新的。
	require.NoError(t, os.Chmod(path, 0o644))
	_, _, err = EnsureSigningKeyFile(path)
	require.Error(t, err)
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.WriteFile(path, []byte("corrupt\n"), 0o600))
	_, _, err = EnsureSigningKeyFile(path)
	require.Error(t, err)
}

// 操作电脑只存公开的信任信息（控制机与 CA 的指纹、根证书）；已经记了另一套 CA 时拒绝覆盖；清空后才能换。
func TestOperatorTrustPinsOneControlCA(t *testing.T) {
	directory := filepath.Join(privateParent(t), "moox")
	_, _, err := EnsureSigningKeyFile(filepath.Join(directory, OperatorKeyFile))
	require.NoError(t, err)
	first := newFixture(t, "control", true)
	require.NoError(t, InstallOperatorTrust(directory, first.metadata, first.files[hostbundle.CAFile]))
	require.NoError(t, InstallOperatorTrust(directory, first.metadata, first.files[hostbundle.CAFile]), "重跑不报错")
	pin, err := ReadOperatorPin(directory)
	require.NoError(t, err)
	require.Equal(t, OperatorPin{ControlHostID: "control", CA: first.options.ExpectedCA}, pin)
	ca, err := os.ReadFile(filepath.Join(directory, OperatorCAFile))
	require.NoError(t, err)
	require.Equal(t, first.files[hostbundle.CAFile], ca)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 3, "只有私钥、指纹与根证书")

	other := newFixture(t, "control", true)
	require.Error(t, InstallOperatorTrust(directory, other.metadata, other.files[hostbundle.CAFile]), "另一套 CA")
	require.Error(t, InstallOperatorTrust(directory, first.metadata, other.files[hostbundle.CAFile]), "根证书和清单对不上")

	// 全新的控制机：清掉指纹与根证书，私钥留着（新控制机登记的就是它）。
	require.NoError(t, RemoveOperatorTrust(directory))
	_, err = ReadOperatorPin(directory)
	require.Error(t, err)
	_, err = ReadOperatorKey(directory)
	require.NoError(t, err)
	require.NoError(t, InstallOperatorTrust(directory, other.metadata, other.files[hostbundle.CAFile]))
	// 清空全部主机之后：连私钥一起删。
	require.NoError(t, RemoveOperatorIdentity(directory))
	_, err = ReadOperatorKey(directory)
	require.Error(t, err)
	require.NoError(t, RemoveOperatorIdentity(directory), "没有东西可删时不报错")
}
