package release

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RuntimeScripts 是发布目录中的运行脚本，来自仓库的 deploy/runtime。
var RuntimeScripts = []string{
	"install.sh", "start.sh", "stop.sh", "restart.sh", "status.sh", "healthcheck.sh", "pause.sh", "resume.sh",
	"lib/runtime.sh", "lib/log-rotate.sh",
}

// ArchiveInput 是打包一台主机的发布需要的内容。
type ArchiveInput struct {
	Plan      Plan
	ReleaseID string
	// RuntimeDir 是仓库中的 deploy/runtime。
	RuntimeDir string
	// Binaries 是放进 bin/ 的二进制：文件名 → 本机路径。只部署部分组件时只包含这些组件的二进制，
	// 其余由安装器从当前发布复用。
	Binaries map[string]string
	// Incoming 是要装进部署根目录的密钥与证书，路径以 secrets/ 或 certs/ 开头。
	Incoming []File
	// CreatedAt 写入 RELEASE；为零时取当前时间。
	CreatedAt time.Time
}

// releaseInfo 是发布目录中的 RELEASE 文件。
type releaseInfo struct {
	Release    string    `json:"release"`
	Host       string    `json:"host"`
	Components []string  `json:"components"`
	CreatedAt  time.Time `json:"created_at"`
}

// WriteArchive 把一台主机的发布写成 tar.gz。
func WriteArchive(w io.Writer, in ArchiveInput) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	modTime := in.CreatedAt
	if modTime.IsZero() {
		modTime = time.Now().UTC()
	}
	add := func(name string, mode fs.FileMode, data []byte) error {
		header := &tar.Header{Name: name, Mode: int64(mode.Perm()), Size: int64(len(data)), ModTime: modTime, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	addPath := func(name string, mode fs.FileMode, path string) error {
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("打开 %s: %w", path, err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("%s 不是普通文件", path)
		}
		header := &tar.Header{Name: name, Mode: int64(mode.Perm()), Size: info.Size(), ModTime: modTime, Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		_, err = io.Copy(tw, file)
		return err
	}
	for _, script := range RuntimeScripts {
		if err := addPath(script, 0o755, filepath.Join(in.RuntimeDir, filepath.FromSlash(script))); err != nil {
			return err
		}
	}
	for _, file := range in.Plan.Files {
		if err := add(file.Path, file.Mode, file.Data); err != nil {
			return err
		}
	}
	var binaries []string
	seen := map[string]bool{}
	for _, component := range in.Plan.Components {
		for _, binary := range component.Binaries {
			if !seen[binary] {
				seen[binary] = true
				binaries = append(binaries, binary)
			}
		}
	}
	sort.Strings(binaries)
	if err := add("runtime/binaries", 0o644, []byte(strings.Join(binaries, "\n")+"\n")); err != nil {
		return err
	}
	names := make([]string, 0, len(in.Binaries))
	for name := range in.Binaries {
		if !seen[name] {
			return fmt.Errorf("发布中没有用到二进制 %s", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := addPath("bin/"+name, 0o755, in.Binaries[name]); err != nil {
			return err
		}
	}
	for _, file := range in.Incoming {
		if !strings.HasPrefix(file.Path, "secrets/") && !strings.HasPrefix(file.Path, "certs/") {
			return fmt.Errorf("密钥与证书的路径必须以 secrets/ 或 certs/ 开头：%s", file.Path)
		}
		if err := add("incoming/"+file.Path, 0o600, file.Data); err != nil {
			return err
		}
	}
	info, err := json.MarshalIndent(releaseInfo{
		Release: in.ReleaseID, Host: in.Plan.HostID, Components: in.Plan.ComponentIDs(), CreatedAt: modTime,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := add("RELEASE", 0o644, append(info, '\n')); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
