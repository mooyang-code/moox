package collectorpackager

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"gopkg.in/yaml.v3"
)

// BuildSCFPackageOptions configures a Tencent SCF package build.
type BuildSCFPackageOptions struct {
	BinaryPath               string
	ConfigDir                string
	OutPath                  string
	StoragePrimaryAuthSecret string
}

// BuildSCFPackageResult describes the created package.
type BuildSCFPackageResult struct {
	Path    string
	Entries []string
}

// BuildSCFPackage creates a zip containing only the short-lived runtime binary,
// configuration, source definitions, and the stock trading calendar when the
// stock_cn profile is packaged. CLS credentials are injected as SCF environment
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
			if filepath.ToSlash(rel) == "sources/market/binance.yaml" {
				if err := addRenderedStorageAuthConfig(zw, path, rel, opts.StoragePrimaryAuthSecret); err != nil {
					return err
				}
				entries = append(entries, filepath.ToSlash(rel))
				return nil
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
		if err := addFile(calendarPath, "markets/stock_cn/calendar.yaml"); err != nil {
			return nil, err
		}
	}

	sort.Strings(entries)
	return &BuildSCFPackageResult{Path: opts.OutPath, Entries: entries}, nil
}

func stockCNCalendarPath(configDir string) (string, error) {
	if filepath.Base(filepath.Clean(configDir)) != "stock_cn" {
		return "", nil
	}
	candidate := filepath.Clean(filepath.Join(configDir, "..", "..", "..", "config", "markets", "stock_cn", "calendar.yaml"))
	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("stock_cn calendar is required at %s: %w", candidate, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("stock_cn calendar is required at %s: path is a directory", candidate)
	}
	return candidate, nil
}

func addRenderedStorageAuthConfig(zw *zip.Writer, src, dst, secret string) error {
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("MOOX_STORAGE_PRIMARY_AUTH_SECRET is required to package Binance Storage credentials")
	}
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return fmt.Errorf("parse Binance source config: %w", err)
	}
	rendered := 0
	var visit func(*yaml.Node) error
	visit = func(node *yaml.Node) error {
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				if key.Value == "auth_info" && value.Kind == yaml.MappingNode {
					appID, appKey := mappingValue(value, "app_id"), mappingValue(value, "app_key")
					if appID == nil || strings.TrimSpace(appID.Value) == "" || appKey == nil {
						return fmt.Errorf("Binance Storage auth_info requires app_id and app_key")
					}
					appKey.Value = mooxsecurity.HMACSHA256Hex(secret, []byte(strings.TrimSpace(appID.Value)))
					rendered++
				}
				if err := visit(value); err != nil {
					return err
				}
			}
		} else {
			for _, child := range node.Content {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(&document); err != nil {
		return err
	}
	if rendered == 0 {
		return fmt.Errorf("Binance source config contains no Storage auth_info")
	}
	var output strings.Builder
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return fmt.Errorf("render Binance source config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return addZipBytes(zw, []byte(output.String()), dst, 0o644)
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
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
