package bundle

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// maxBinaryBytes 是单个二进制的上限：Storage 带 DuckDB 约 100 MiB，留足余量。
const maxBinaryBytes = 1 << 30

var (
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	supportedArchs = []string{"amd64", "arm64"}
)

// PackageFileName 是一个组件包的文件名：moox-<组件>_<版本>_linux_<架构>.tar.gz。
func PackageFileName(component, version, goarch string) string {
	return "moox-" + component + "_" + version + "_linux_" + goarch + ".tar.gz"
}

// Binaries 是组件包里 bin/ 下的文件：组件自己的二进制在前，再是配套命令行（服务目录的 binary 与 tools）。
func Binaries(component servicecatalog.Component) []string {
	return append([]string{component.Binary}, component.Tools...)
}

// PackageComponents 为服务目录里的每个组件打一个包，写到 outputDir，返回组件 ID → 包。
//
// 包里只有 bin/<二进制>（0755）。
// 条目按名字排序，时间戳为 0、属主为 0 且不带用户名组名，相同输入得到逐字节相同的包。
// binaryDir 里缺哪个组件的哪个文件就报哪个，不生成残缺的包。
func PackageComponents(catalog servicecatalog.Catalog, _ string, binaryDir, outputDir, version, goarch string) (map[string]Artifact, error) {
	if !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("package_invalid: 版本 %q 只能用字母、数字、点、下划线和连字符", version)
	}
	if !slices.Contains(supportedArchs, goarch) {
		return nil, fmt.Errorf("package_invalid: 架构 %q 不支持，只能是 amd64 或 arm64", goarch)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("package_invalid: 建不了输出目录 %s：%w", outputDir, err)
	}
	result := make(map[string]Artifact, len(catalog.Components))
	for _, component := range catalog.Components {
		entries := map[string]packageEntry{}
		artifact := Artifact{File: PackageFileName(component.ID, version, goarch), Binaries: map[string]string{}}
		for _, name := range Binaries(component) {
			source := filepath.Join(binaryDir, name)
			digest, err := regularFileDigest(source)
			if err != nil {
				return nil, fmt.Errorf("package_binary_missing: 组件 %s 缺 %s（%s）：先用 setup build-linux 或 build-linux-local.sh 编译：%w", component.ID, name, source, err)
			}
			entries["bin/"+name] = packageEntry{source: source, mode: 0o755}
			artifact.Binaries[name] = digest
		}
		digest, size, err := writePackage(filepath.Join(outputDir, artifact.File), entries)
		if err != nil {
			return nil, fmt.Errorf("package_write_failed: 组件 %s：%w", component.ID, err)
		}
		artifact.SHA256, artifact.Size = digest, size
		result[component.ID] = artifact
	}
	return result, nil
}

type packageEntry struct {
	source string
	mode   int64
}

// regularFileDigest 要求是非空的普通文件（不跟随符号链接），返回内容摘要。
func regularFileDigest(name string) (string, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBinaryBytes {
		return "", errors.New("不是非空的普通文件")
	}
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// writePackage 先写临时文件再改名，返回包的摘要与大小。
func writePackage(output string, entries map[string]packageEntry) (string, int64, error) {
	temporary, err := os.CreateTemp(filepath.Dir(output), ".moox-package-*.tar.gz")
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = temporary.Close(); _ = os.Remove(temporary.Name()) }()
	hash := sha256.New()
	// gzip 头不写文件名和时间，压缩级别固定，同一 Go 版本下输出确定。
	compressed := gzip.NewWriter(io.MultiWriter(temporary, hash))
	archive := tar.NewWriter(compressed)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := addEntry(archive, name, entries[name]); err != nil {
			return "", 0, err
		}
	}
	if err := errors.Join(archive.Close(), compressed.Close()); err != nil {
		return "", 0, err
	}
	if err := temporary.Sync(); err != nil {
		return "", 0, err
	}
	info, err := temporary.Stat()
	if err != nil {
		return "", 0, err
	}
	if err := temporary.Close(); err != nil {
		return "", 0, err
	}
	if err := os.Chmod(temporary.Name(), 0o644); err != nil {
		return "", 0, err
	}
	if err := os.Rename(temporary.Name(), output); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), info.Size(), nil
}

func addEntry(archive *tar.Writer, name string, entry packageEntry) error {
	file, err := os.Open(entry.source)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	header := &tar.Header{
		Typeflag: tar.TypeReg, Name: name, Mode: entry.mode, Size: info.Size(),
		ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
	}
	if err := archive.WriteHeader(header); err != nil {
		return err
	}
	written, err := io.Copy(archive, file)
	if err != nil {
		return err
	}
	if written != info.Size() {
		return fmt.Errorf("%s 在打包时被改动", entry.source)
	}
	return nil
}
