// Package bundle 读取发布产物包（moox-bundle）的清单。
//
// 产物包是某个提交的仓库文件快照加上预编译的 Linux 二进制：用户解压后在包目录里运行 moox-cli，
// 部署与初始化直接使用包内的二进制和公共模板，不需要 git、Go、Docker 或 Node。
// 清单列出快照里的全部文件，代替 git 的跟踪文件列表；二进制目录不在列表里。
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
	// BinaryDirectory 是相对包根的 Linux 二进制目录。
	BinaryDirectory string `json:"binary_directory"`
	// Files 是快照里的全部文件（相对包根、斜杠分隔），等价于该提交的 git ls-files。
	Files []string `json:"files"`
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

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
	if manifest.Version != 1 || manifest.Release == "" || !commitPattern.MatchString(manifest.Commit) || !validPath(manifest.BinaryDirectory) || len(manifest.Files) == 0 {
		return manifest, true, errors.New("产物包清单不完整")
	}
	for _, name := range manifest.Files {
		if !validPath(name) {
			return manifest, true, fmt.Errorf("产物包清单里有非法路径 %q", name)
		}
	}
	return manifest, true, nil
}

// BinaryPath 返回包内二进制目录的绝对路径。
func (m Manifest) BinaryPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(m.BinaryDirectory))
}

func validPath(name string) bool {
	return name != "" && name != "." && path.Clean(name) == name && !path.IsAbs(name) && name != ".." &&
		!strings.HasPrefix(name, "../") && !strings.ContainsAny(name, "\\\x00\r\n")
}
