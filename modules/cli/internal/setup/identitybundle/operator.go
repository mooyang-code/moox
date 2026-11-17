package identitybundle

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

// 操作电脑身份目录（~/.config/moox）里的文件。
const (
	// OperatorKeyFile 是 moox-cli 的签名私钥，只在这台电脑上生成、保存。
	OperatorKeyFile = "moox-cli.key"
	// OperatorPinFile 记下控制机与 MooX CA 的指纹；OperatorCAFile 是按这个指纹校验过的根证书。
	OperatorPinFile = "gateway-bootstrap.json"
	OperatorCAFile  = "moox-ca.crt"
)

// OperatorPin 是第一次部署控制机时记下的公开信息：之后取别的主机的身份包都按这个 CA 校验。
type OperatorPin struct {
	ControlHostID string `json:"control_host_id"`
	CA            string `json:"ca_sha256"`
}

func ReadOperatorPin(directory string) (OperatorPin, error) {
	var pin OperatorPin
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return pin, err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, OperatorPinFile, 4096)
	if err != nil {
		return pin, err
	}
	if err := canonical(raw, &pin); err != nil {
		return pin, err
	}
	if pin.ControlHostID == "" || !validHash(pin.CA) {
		return pin, errors.New("operator bootstrap pin is invalid")
	}
	return pin, nil
}

// InstallOperatorTrust 把已校验的控制机身份包里的根证书与它的指纹存到这台电脑。已有的指纹属于另一套 CA 时拒绝：
// 换部署要先清掉旧的（setup wipe 会做）。
func InstallOperatorTrust(directory string, source hostbundle.Metadata, ca []byte) error {
	if _, err := certificate(ca, source.CA.CertificateInfo); err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return err
	}
	defer root.Close()
	pin, err := json.Marshal(OperatorPin{ControlHostID: source.ControlHostID, CA: source.CA.SHA256})
	if err != nil {
		return err
	}
	pin = append(pin, '\n')
	if _, err := root.Lstat(OperatorPinFile); err == nil {
		old, err := fsutil.ReadPrivate(root, OperatorPinFile, 4096)
		if err != nil || !bytes.Equal(old, pin) {
			return errors.New("installed operator identity belongs to another control CA")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	} else if err := fsutil.WritePrivate(root, OperatorPinFile, pin, false); err != nil {
		return err
	}
	// 操作端经 control 中转连接只能出站的主机时用这份根证书校验证书。
	return fsutil.WritePrivate(root, OperatorCAFile, ca, true)
}

// RemoveOperatorTrust 删除这台电脑上记的控制机指纹与根证书。全新的控制机会生成新的 CA，旧的对不上。
// moox-cli 的私钥不动：新的控制机 bootstrap 时登记的就是它的公钥。
func RemoveOperatorTrust(directory string) error {
	return removeFiles(directory, OperatorPinFile, OperatorCAFile)
}

// RemoveOperatorIdentity 删除这台电脑上的全部操作员身份（清空全部主机之后）：指纹、根证书与 moox-cli 的私钥。
func RemoveOperatorIdentity(directory string) error {
	return removeFiles(directory, OperatorPinFile, OperatorCAFile, OperatorKeyFile)
}

func removeFiles(directory string, names ...string) error {
	for _, name := range names {
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ReadOperatorKey 读这台电脑上 moox-cli 的签名私钥。
func ReadOperatorKey(directory string) (ed25519.PrivateKey, error) {
	return gatewayauth.ReadSigningKey(filepath.Join(directory, OperatorKeyFile))
}

// EnsureSigningKeyFile 读一个签名私钥文件；没有时生成一把写进去（0600，所在目录 0700）。已有的文件原样复用，
// 所以重跑不会换掉密钥。created 报告这次是不是新生成的。
func EnsureSigningKeyFile(path string) (key ed25519.PrivateKey, created bool, err error) {
	if _, statErr := os.Lstat(path); statErr == nil {
		key, err = gatewayauth.ReadSigningKey(path)
		if err != nil {
			return nil, false, fmt.Errorf("已有的签名私钥 %s 读不出来（%w）；确认不再需要它之后删掉再重跑", path, err)
		}
		return key, false, nil
	} else if !os.IsNotExist(statErr) {
		return nil, false, statErr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	if key, err = gatewayauth.GenerateSigningKey(); err != nil {
		return nil, false, err
	}
	// O_EXCL：两个命令同时生成时只有一个成功，另一个报错后重跑会读到同一把。
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, false, err
	}
	_, writeErr := file.WriteString(gatewayauth.EncodeSigningKey(key) + "\n")
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		_ = os.Remove(path)
		return nil, false, err
	}
	return key, true, nil
}

// PublicKey 返回私钥对应公钥的 base64 写法。
func PublicKey(key ed25519.PrivateKey) string {
	return gatewayauth.EncodePublicKey(gatewayauth.Credentials{Key: key}.PublicKey())
}
