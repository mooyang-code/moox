package deploy

import (
	"encoding/json"
	"maps"
)

// Version 是部署根下 version.json 的内容：这台主机最近一次部署的版本，以及每个组件装的二进制与配置指纹。
type Version struct {
	Version       string                      `json:"version"`
	Commit        string                      `json:"commit"`
	CatalogSHA256 string                      `json:"catalog_sha256"`
	DeployedAt    string                      `json:"deployed_at"`
	Components    map[string]ComponentVersion `json:"components"`
}

// ComponentVersion 是一个组件装的东西：bin/ 下每个文件的摘要，以及配置、环境文件、单元文件与秘密的指纹。
type ComponentVersion struct {
	Binaries    map[string]string `json:"binaries"`
	Fingerprint string            `json:"fingerprint"`
}

// ParseVersion 解析目标机带回的 version.json；空内容返回 nil（没装过）。
func ParseVersion(raw []byte) (*Version, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var version Version
	if err := json.Unmarshal(raw, &version); err != nil {
		return nil, err
	}
	return &version, nil
}

// changed 报告组件相对上次部署记录有没有变化（没有记录算有变化）。
func (v *Version) changed(id string, next ComponentVersion) bool {
	if v == nil {
		return true
	}
	previous, ok := v.Components[id]
	return !ok || previous.Fingerprint != next.Fingerprint || !maps.Equal(previous.Binaries, next.Binaries)
}

func (v *Version) known(id string) bool {
	if v == nil {
		return false
	}
	_, ok := v.Components[id]
	return ok
}
