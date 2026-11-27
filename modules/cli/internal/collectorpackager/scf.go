package collectorpackager

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BuildSCFPackageOptions configures a Tencent SCF package build.
type BuildSCFPackageOptions struct {
	BinaryPath string
	OutPath    string
}

// BuildSCFPackageResult describes the created package.
type BuildSCFPackageResult struct {
	Path    string
	Entries []string
}

// BuildSCFPackage 打出仅含单一 Linux ELF 可执行文件 main 的 SCF 代码包 zip。
// 各市场的 market.yaml、route.yaml、数据源运行参数（含 binance.yaml）和交易日历均内嵌在二进制内，
// 不随包分发任何外部配置文件；CLS 与网关凭据由云函数环境变量注入。
func BuildSCFPackage(opts BuildSCFPackageOptions) (*BuildSCFPackageResult, error) {
	if opts.BinaryPath == "" {
		return nil, fmt.Errorf("binary path is required")
	}
	if opts.OutPath == "" {
		return nil, fmt.Errorf("output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(opts.OutPath), 0o755); err != nil {
		return nil, err
	}
	out, err := os.CreateTemp(filepath.Dir(opts.OutPath), ".scf-package-*.zip")
	if err != nil {
		return nil, err
	}
	tempPath := out.Name()
	defer os.Remove(tempPath)
	defer out.Close()
	if err := out.Chmod(0o600); err != nil {
		return nil, err
	}

	zw := zip.NewWriter(out)
	if err := addZipFile(zw, opts.BinaryPath, "main"); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if err := out.Close(); err != nil {
		return nil, err
	}
	if err := ValidateSCFPackageZip(tempPath); err != nil {
		return nil, err
	}
	if err := os.Rename(tempPath, opts.OutPath); err != nil {
		return nil, err
	}
	return &BuildSCFPackageResult{Path: opts.OutPath, Entries: []string{"main"}}, nil
}

// ValidateSCFPackageZip gates built and external packages before upload:
// only a single executable Linux amd64/arm64 ELF binary named "main" is permitted.
func ValidateSCFPackageZip(zipPath string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open scf package zip: %w", err)
	}
	defer reader.Close()
	seen := make(map[string]bool)
	validMain := false
	for _, file := range reader.File {
		name := file.Name
		if name != filepath.ToSlash(name) || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") ||
			filepath.ToSlash(filepath.Clean(name)) != strings.TrimSuffix(name, "/") || strings.HasPrefix(name, "../") {
			return fmt.Errorf("invalid scf package entry path")
		}
		if seen[name] {
			return fmt.Errorf("duplicate scf package entry %s", name)
		}
		seen[name] = true
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("scf package symlinks are not permitted")
		}
		if name == "main" {
			if err := validateSCFMain(file); err != nil {
				return err
			}
			validMain = true
			continue
		}
		if file.FileInfo().IsDir() {
			continue
		}
		return fmt.Errorf("unexpected scf package payload %s", name)
	}
	if !validMain {
		return fmt.Errorf("scf package requires an executable Linux main")
	}
	return nil
}

var scfBinaryPrivateMaterial = regexp.MustCompile(`-----BEGIN (?:(?:[A-Z0-9]+ )?PRIVATE KEY|NATS USER JWT|USER NKEY SEED)-----[\r\n]`)
var scfBinaryCredentialAssignment = regexp.MustCompile(`(?mi)(?:^|[\x00\r\n])[ \t]*(?:MOOX_EVENTBUS_NATS_PASSWORD|MOOX_GATEWAY_SERVICE_SECRET_KEY|MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY|TENCENTCLOUD_SECRET_KEY|TENCENT_SECRET_KEY)[ \t]*[:=][ \t]*[^\x00\r\n \t]`)

func validateSCFMain(file *zip.File) error {
	if !file.Mode().IsRegular() || file.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("scf package main must be a regular executable file")
	}
	const maxBinaryBytes = 512 << 20
	if file.UncompressedSize64 == 0 || file.UncompressedSize64 > maxBinaryBytes {
		return fmt.Errorf("scf package main has an invalid binary size")
	}
	rc, err := file.Open()
	if err != nil {
		return fmt.Errorf("read scf package main: %w", err)
	}
	defer rc.Close()
	content, err := io.ReadAll(io.LimitReader(rc, maxBinaryBytes+1))
	if err != nil || len(content) > maxBinaryBytes {
		return fmt.Errorf("cannot validate scf package main")
	}
	if scfBinaryPrivateMaterial.Match(content) || scfBinaryCredentialAssignment.Match(content) {
		return fmt.Errorf("scf package main contains private key or credential material")
	}
	binary, err := elf.NewFile(bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("scf package main must be a Linux amd64 or arm64 ELF executable")
	}
	defer binary.Close()
	if binary.Class != elf.ELFCLASS64 || binary.Data != elf.ELFDATA2LSB ||
		(binary.Machine != elf.EM_X86_64 && binary.Machine != elf.EM_AARCH64) ||
		(binary.Type != elf.ET_EXEC && binary.Type != elf.ET_DYN) ||
		(binary.OSABI != elf.ELFOSABI_NONE && binary.OSABI != elf.ELFOSABI_LINUX) {
		return fmt.Errorf("scf package main must be a Linux amd64 or arm64 ELF executable")
	}
	for _, program := range binary.Progs {
		if program.Type == elf.PT_LOAD && program.Flags&elf.PF_X != 0 &&
			program.Off <= uint64(len(content)) && program.Filesz <= uint64(len(content))-program.Off &&
			binary.Entry >= program.Vaddr && binary.Entry-program.Vaddr < program.Filesz {
			return nil
		}
	}
	return fmt.Errorf("scf package main ELF entry point must be in an executable load segment")
}

func addZipFile(zw *zip.Writer, src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("scf package source symlinks are not permitted: %s", src)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("scf package source must be a regular file: %s", src)
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(dst)
	header.Method = zip.Deflate

	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(w, in)
	return err
}

func addZipBytes(zw *zip.Writer, content []byte, dst string, mode os.FileMode) error {
	header := &zip.FileHeader{Name: filepath.ToSlash(dst), Method: zip.Deflate}
	header.SetMode(mode)
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = writer.Write(content)
	return err
}
