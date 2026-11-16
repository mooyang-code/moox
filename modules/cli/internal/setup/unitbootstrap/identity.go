package unitbootstrap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/failure"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
)

type runtimeBinding struct {
	Version int    `json:"version"`
	HostID  string `json:"host_id"`
	SHA256  string `json:"sha256"`
}

// 在任何进程停下之前，把这次初始化的运行标识绑定到发布快照之外。本机状态丢失不授权替换已初始化部署的标识。
// 运行标识是操作电脑首次安装时生成的随机值，不是密钥（健康端口与管理台登录都不再需要部署时下发的密钥）。
func bindRuntimeIdentity(deployment *os.Root, input inputs) error {
	value := input.request.RuntimeID
	if len(value) < 32 {
		return errors.New("bootstrap requires a valid persistent runtime identity")
	}
	if len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("bootstrap runtime identity must be bounded canonical text")
	}
	raw, err := json.Marshal(map[string]string{"runtime_id": value})
	if err != nil {
		return err
	}
	wanted := runtimeBinding{Version: 1, HostID: input.request.Topology.ControlHostID, SHA256: hash(raw)}
	if _, err := deployment.Lstat("identity/runtime.json"); err == nil {
		identity, err := fsutil.OpenPhysicalRoot(filepath.Join(deployment.Name(), "identity"), true)
		if err != nil {
			return err
		}
		defer identity.Close()
		raw, err := fsutil.ReadPrivate(identity, "runtime.json", 4096)
		if err != nil {
			return err
		}
		var stored runtimeBinding
		if decode(raw, &stored) != nil || stored != wanted {
			return failure.New(failure.TargetIdentityDiffers, "runtime identity differs from the persistent deployment binding; restore original native state")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, unit := range []Unit{input.request.Host, input.request.Control} {
		root, err := fsutil.OpenPhysicalRoot(unit.UnitRoot, false)
		if err != nil {
			return err
		}
		_, statErr := root.Lstat("current")
		root.Close()
		if !os.IsNotExist(statErr) {
			return failure.New(failure.TargetBindingMissing, "existing deployment lacks its persistent runtime identity binding")
		}
	}
	if err := privateDirectory(deployment, "identity"); err != nil {
		return err
	}
	raw, err = json.Marshal(wanted)
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(deployment, "identity/runtime.json", append(raw, '\n'), false)
}
