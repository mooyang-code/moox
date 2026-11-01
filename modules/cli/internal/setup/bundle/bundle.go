// Package bundle 读取发布产物包（moox-bundle）的清单。
//
// 产物包是某个提交的仓库文件快照加上各发布附件的清单：用户解压后在包目录里运行 moox-cli，
// 目标机按清单下载单元包并校验，不需要 git、Go、Docker 或 Node。
// 清单列出快照里的全部文件，代替 git 的跟踪文件列表。
package bundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// ManifestName 是产物包根目录下的清单文件名。
const ManifestName = "moox-bundle.json"

// Manifest 是产物包清单。
type Manifest struct {
	Version int `json:"version"`
	// Release 是发布版本号，例如 v0.2.0。
	Release string `json:"release"`
	// Commit 是快照对应的完整提交。
	Commit string `json:"commit"`
	// CatalogSHA256 是打包时的服务目录摘要，和 CLI 不一致说明包和命令行不是同一版本。
	CatalogSHA256 string `json:"catalog_sha256"`
	// Units 是各部署单元包，键是单元名。
	Units map[string]Artifact `json:"units"`
	// CollectorSCF 是云函数程序。
	CollectorSCF Artifact `json:"collector_scf"`
	// Files 是快照里的全部文件（相对包根、斜杠分隔），等价于该提交的 git ls-files。
	Files []string `json:"files"`
}

// Artifact 是一个随发布下载的附件。
type Artifact struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Manifest 是单元包的内容清单（unitpackage.Manifest），由使用方解析；云函数程序没有。
	Manifest json.RawMessage `json:"manifest,omitempty"`
}

var (
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	releasePattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
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
	if manifest.Version != 1 || manifest.Release == "" || !commitPattern.MatchString(manifest.Commit) || len(manifest.Units) == 0 || len(manifest.Files) == 0 {
		return manifest, true, errors.New("产物包清单不完整")
	}
	for _, name := range manifest.Files {
		if !validPath(name) {
			return manifest, true, fmt.Errorf("产物包清单里有非法路径 %q", name)
		}
	}
	return manifest, true, nil
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

func validPath(name string) bool {
	return name != "" && name != "." && path.Clean(name) == name && !path.IsAbs(name) && name != ".." &&
		!strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00\r\n")
}
