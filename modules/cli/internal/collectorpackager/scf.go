package collectorpackager

import (
	"archive/zip"
	"bytes"
	"crypto/x509"
	"debug/elf"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// BuildSCFPackageOptions configures a Tencent SCF package build.
type BuildSCFPackageOptions struct {
	BinaryPath    string
	ConfigDir     string
	OutPath       string
	EventBusCAPEM []byte
}

// BuildSCFPackageResult describes the created package.
type BuildSCFPackageResult struct {
	Path    string
	Entries []string
}

// BuildSCFPackage creates a zip containing only the short-lived runtime binary,
// configuration, source definitions, and the stock trading calendar when the
// stockcn profile is packaged. CLS credentials are injected as SCF environment
// variables and never rendered into the package.
func BuildSCFPackage(opts BuildSCFPackageOptions) (*BuildSCFPackageResult, error) {
	if opts.BinaryPath == "" {
		return nil, fmt.Errorf("binary path is required")
	}
	if opts.ConfigDir == "" {
		return nil, fmt.Errorf("config dir is required")
	}
	if opts.OutPath == "" {
		return nil, fmt.Errorf("output path is required")
	}
	if err := validatePublicCA(opts.EventBusCAPEM); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(opts.OutPath), 0o755); err != nil {
		return nil, err
	}
	out, err := os.OpenFile(opts.OutPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	if err := out.Chmod(0o600); err != nil {
		return nil, err
	}

	zw := zip.NewWriter(out)
	defer zw.Close()

	var entries []string
	if err := addZipBytes(zw, opts.EventBusCAPEM, "certs/eventbus-ca.pem", 0o644); err != nil {
		return nil, err
	}
	entries = append(entries, "certs/eventbus-ca.pem")
	addFile := func(src, dst string) error {
		if err := addZipFile(zw, src, dst); err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(dst))
		return nil
	}

	if err := addFile(opts.BinaryPath, "main"); err != nil {
		return nil, err
	}

	configPath := filepath.Join(opts.ConfigDir, "config.yaml")
	if err := addFile(configPath, "config.yaml"); err != nil {
		return nil, err
	}

	sourcesDir := filepath.Join(opts.ConfigDir, "sources")
	if _, err := os.Stat(sourcesDir); err == nil {
		err = filepath.WalkDir(sourcesDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(opts.ConfigDir, path)
			if err != nil {
				return err
			}
			return addFile(path, rel)
		})
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	calendarPath, err := stockCNCalendarPath(opts.ConfigDir)
	if err != nil {
		return nil, err
	}
	if calendarPath != "" {
		if err := addFile(calendarPath, "markets/stockcn/calendar.yaml"); err != nil {
			return nil, err
		}
	}

	routePath, err := stockCNRoutePath(opts.ConfigDir)
	if err != nil {
		return nil, err
	}
	if routePath != "" {
		if err := addFile(routePath, "markets/stockcn/route.yaml"); err != nil {
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	if err := ValidateSCFPackageZip(opts.OutPath); err != nil {
		return nil, err
	}
	sort.Strings(entries)
	return &BuildSCFPackageResult{Path: opts.OutPath, Entries: entries}, nil
}

// ValidateSCFPackageZip gates built and external packages before upload.
func ValidateSCFPackageZip(zipPath string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open scf package zip: %w", err)
	}
	defer reader.Close()
	seen := make(map[string]bool)
	validCA := false
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
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("scf package symlinks are not permitted")
		}
		if name != "certs/eventbus-ca.pem" && name != "config.yaml" &&
			!((strings.HasPrefix(name, "sources/") || strings.HasPrefix(name, "markets/")) && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml"))) {
			return fmt.Errorf("unexpected scf package payload %s", name)
		}
		rc, err := file.Open()
		if err != nil {
			return fmt.Errorf("read scf package entry %s: %w", name, err)
		}
		content, err := io.ReadAll(io.LimitReader(rc, 4*1024*1024+1))
		_ = rc.Close()
		if err != nil || len(content) > 4*1024*1024 {
			return fmt.Errorf("cannot validate scf package entry %s", name)
		}
		if name == "certs/eventbus-ca.pem" {
			if err := validatePublicCA(content); err != nil {
				return err
			}
			validCA = true
		} else if err := validateCredentialFreeYAML(content); err != nil {
			return fmt.Errorf("scf package entry %s: %w", name, err)
		}
	}
	if !validCA {
		return fmt.Errorf("scf package requires certs/eventbus-ca.pem")
	}
	if !validMain {
		return fmt.Errorf("scf package requires an executable Linux main")
	}
	return nil
}

var scfBinaryPrivateMaterial = regexp.MustCompile(`-----BEGIN (?:(?:[A-Z0-9]+ )?PRIVATE KEY|NATS USER JWT|USER NKEY SEED)-----[\r\n]`)
var scfBinaryCredentialAssignment = regexp.MustCompile(`(?mi)(?:^|[\x00\r\n])[ \t]*(?:MOOX_STORAGE_PRIMARY_AUTH_SECRET|MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON|MOOX_EVENTBUS_NATS_PASSWORD|MOOX_GATEWAY_SERVICE_SECRET_KEY|MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY|TENCENTCLOUD_SECRET_KEY|TENCENT_SECRET_KEY)[ \t]*[:=][ \t]*[^\x00\r\n \t]`)

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

func ValidateEventBusCAPEM(content []byte) error {
	return validatePublicCA(content)
}

func validatePublicCA(content []byte) error {
	count := 0
	for remaining := bytes.TrimSpace(content); len(remaining) != 0; {
		if !bytes.HasPrefix(remaining, []byte("-----BEGIN CERTIFICATE-----")) {
			return fmt.Errorf("certs/eventbus-ca.pem must contain only public CA certificates")
		}
		endMarker := []byte("-----END CERTIFICATE-----")
		end := bytes.Index(remaining, endMarker)
		if end < 0 {
			return fmt.Errorf("certs/eventbus-ca.pem contains a malformed certificate")
		}
		end += len(endMarker)
		block, rest := pem.Decode(remaining[:end])
		if block == nil || len(bytes.TrimSpace(rest)) != 0 || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return fmt.Errorf("certs/eventbus-ca.pem must contain only public CA certificates")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid {
			return fmt.Errorf("certs/eventbus-ca.pem contains an invalid CA certificate")
		}
		if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return fmt.Errorf("certs/eventbus-ca.pem CA certificate must permit certificate signing")
		}
		now := time.Now()
		if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
			return fmt.Errorf("certs/eventbus-ca.pem CA certificate is outside its validity period")
		}
		count++
		remaining = bytes.TrimSpace(remaining[end:])
	}
	if count == 0 {
		return fmt.Errorf("certs/eventbus-ca.pem requires a public CA certificate")
	}
	return nil
}

func validateCredentialFreeYAML(content []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("invalid configuration YAML")
		}
		var visit func(*yaml.Node, string) error
		visit = func(node *yaml.Node, path string) error {
			if node.Kind == yaml.AliasNode {
				return fmt.Errorf("configuration YAML aliases are not permitted")
			}
			if node.Kind == yaml.ScalarNode && strings.Contains(strings.ToUpper(node.Value), "PRIVATE KEY") {
				return fmt.Errorf("private key payload is not permitted")
			}
			if node.Kind == yaml.MappingNode {
				keys := make(map[string]bool)
				for i := 0; i+1 < len(node.Content); i += 2 {
					key, value := node.Content[i], node.Content[i+1]
					if keys[key.Value] {
						return fmt.Errorf("duplicate configuration key")
					}
					keys[key.Value] = true
					name := strings.ToLower(strings.ReplaceAll(key.Value, "-", "_"))
					fieldPath := path + "." + key.Value
					sensitive := name == "app_key" || name == "key" || name == "token" ||
						strings.Contains(name, "secret") || strings.Contains(name, "password") ||
						strings.Contains(name, "private_key") || strings.HasSuffix(name, "api_key") ||
						strings.HasSuffix(name, "hmac_key_file") || strings.HasSuffix(name, "app_keys_json") ||
						strings.HasSuffix(name, "_token") || (strings.HasSuffix(name, "access_key") && fieldPath != ".system.service_auth.access_key")
					if sensitive && (value.Kind != yaml.ScalarNode || strings.TrimSpace(value.Value) != "" && value.Tag != "!!null") {
						return fmt.Errorf("nonempty credential field %s is not permitted", key.Value)
					}
					if err := visit(value, fieldPath); err != nil {
						return err
					}
				}
				return nil
			}
			for _, child := range node.Content {
				if err := visit(child, path); err != nil {
					return err
				}
			}
			return nil
		}
		if err := visit(&document, ""); err != nil {
			return err
		}
	}
}

func stockCNCalendarPath(configDir string) (string, error) {
	if filepath.Base(filepath.Clean(configDir)) != "stockcn" {
		return "", nil
	}
	candidate := filepath.Clean(filepath.Join(configDir, "..", "..", "..", "config", "markets", "stockcn", "calendar.yaml"))
	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("stockcn calendar is required at %s: %w", candidate, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("stockcn calendar is required at %s: path is a directory", candidate)
	}
	return candidate, nil
}

func stockCNRoutePath(configDir string) (string, error) {
	if filepath.Base(filepath.Clean(configDir)) != "stockcn" {
		return "", nil
	}
	candidate := filepath.Clean(filepath.Join(configDir, "..", "..", "..", "config", "markets", "stockcn", "route.yaml"))
	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("stockcn route is required at %s: %w", candidate, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("stockcn route is required at %s: path is a directory", candidate)
	}
	return candidate, nil
}

func addZipFile(zw *zip.Writer, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
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
