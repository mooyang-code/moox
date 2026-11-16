// Package bundle 读写发布产物包（moox-bundle）的清单，并按组件打包服务器二进制。
//
// 产物包是某个提交的仓库文件快照加上各发布附件的清单：用户解压后在包目录里运行 moox-cli，
// 目标机按清单下载组件包并校验，不需要 git、Go、Docker 或 Node。
// 清单列出快照里的全部文件，代替 git 的跟踪文件列表。
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// ManifestName 是产物包根目录下的清单文件名。
const ManifestName = "moox-bundle.json"

// ManifestVersion 是清单格式版本：2 起按组件出包（components），不再有部署单元（units）。
const ManifestVersion = 2

// Manifest 是产物包清单。
type Manifest struct {
	Version int `json:"version"`
	// Release 是发布版本号，例如 v0.2.0。
	Release string `json:"release"`
	// Commit 是快照对应的完整提交。
	Commit string `json:"commit"`
	// CatalogSHA256 是打包时的服务目录摘要（sha256:<hex>），和 CLI 不一致说明包和命令行不是同一版本。
	CatalogSHA256 string `json:"catalog_sha256"`
	// Components 是各组件的包，键是服务目录里的组件 ID。
	Components map[string]Artifact `json:"components"`
	// CollectorSCF 是云函数程序。
	CollectorSCF Artifact `json:"collector_scf"`
	// Files 是快照里的全部文件（相对包根、斜杠分隔），等价于该提交的 git ls-files。
	Files []string `json:"files"`
}

// Artifact 是一个随发布下载的附件。
type Artifact struct {
	File string `json:"file"`
	// SHA256 是附件的摘要（小写十六进制，不带前缀）。
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Binaries 是组件包里 bin/ 下每个文件的摘要（文件名 → 小写十六进制），云函数程序没有。
	Binaries map[string]string `json:"binaries,omitempty"`
}

var (
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	releasePattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Load 读取 root 下的产物包清单；root 不是产物包时返回 false。
func Load(root string) (Manifest, bool, error) {
	var manifest Manifest
	raw, err := os.ReadFile(filepath.Join(root, ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return manifest, false, nil
	}
	if err != nil {
		return manifest, false, err
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, true, fmt.Errorf("产物包清单无法解析: %w", err)
	}
	if manifest.Version != ManifestVersion {
		return manifest, true, fmt.Errorf("bundle_version_mismatch: 产物包清单是 version %d，这个 moox-cli 只认按组件出包的 version %d：换成同一版本的 moox-cli 与产物包", manifest.Version, ManifestVersion)
	}
	if manifest.Release == "" || !commitPattern.MatchString(manifest.Commit) || len(manifest.Components) == 0 || len(manifest.Files) == 0 {
		return manifest, true, errors.New("产物包清单不完整")
	}
	for id, artifact := range manifest.Components {
		if err := artifact.validate(true); err != nil {
			return manifest, true, fmt.Errorf("产物包清单里组件 %s 的包无效：%w", id, err)
		}
	}
	for _, name := range manifest.Files {
		if !validPath(name) {
			return manifest, true, fmt.Errorf("产物包清单里有非法路径 %q", name)
		}
	}
	return manifest, true, nil
}

func (a Artifact) validate(component bool) error {
	if a.File == "" || path.Base(a.File) != a.File || strings.ContainsAny(a.File, "\\\x00\r\n") || a.File == "." || a.File == ".." {
		return fmt.Errorf("文件名 %q 不是单个文件名", a.File)
	}
	if !digestPattern.MatchString(a.SHA256) || a.Size <= 0 {
		return errors.New("摘要或大小无效")
	}
	if component && len(a.Binaries) == 0 {
		return errors.New("没有列出二进制")
	}
	for name, digest := range a.Binaries {
		if name == "" || path.Base(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || !digestPattern.MatchString(digest) {
			return fmt.Errorf("二进制 %q 的名字或摘要无效", name)
		}
	}
	return nil
}

// Component 返回一个组件的包。
func (m Manifest) Component(id string) (Artifact, bool) {
	artifact, ok := m.Components[id]
	return artifact, ok
}

// CheckCLI 确认产物包和 CLI 是同一版本：服务目录必须一致，CLI 是正式版本时版本号也要一致。
func (m Manifest) CheckCLI(version, catalogSHA256 string) error {
	if m.CatalogSHA256 != "" && m.CatalogSHA256 != catalogSHA256 {
		return fmt.Errorf("bundle_version_mismatch: 产物包 %s 的服务目录和这个 moox-cli 不一致，换成同一版本的 CLI", m.Release)
	}
	if releasePattern.MatchString(version) && version != m.Release {
		return fmt.Errorf("bundle_version_mismatch: 产物包是 %s，moox-cli 是 %s，换成同一版本", m.Release, version)
	}
	return nil
}

// CatalogSHA256 是这个 CLI 内嵌服务目录的摘要（sha256:<hex>），写进清单，也用来和清单比对。
func CatalogSHA256() string {
	digest := sha256.Sum256(servicecatalog.EmbeddedYAML())
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validPath(name string) bool {
	return name != "" && name != "." && path.Clean(name) == name && !path.IsAbs(name) && name != ".." &&
		!strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00\r\n")
}
