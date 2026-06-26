package packager

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// BuildSCFPackageOptions configures a Tencent SCF package build.
type BuildSCFPackageOptions struct {
	BinaryPath string
	ConfigDir  string
	OutPath    string
	Version    string
	Overrides  map[string]string
}

// BuildSCFPackageResult describes the created package.
type BuildSCFPackageResult struct {
	Path    string
	Entries []string
}

// BuildSCFPackage creates a zip containing main, config.yaml, optional trpc_go.yaml,
// and nested sources/ entries. The source config files are never modified.
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
	out, err := os.Create(opts.OutPath)
	if err != nil {
		return nil, err
	}
	defer out.Close()

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
	patchedConfig, err := patchedConfigBytes(configPath, opts.Version, opts.Overrides)
	if err != nil {
		return nil, err
	}
	if err := addZipBytes(zw, "config.yaml", patchedConfig); err != nil {
		return nil, err
	}
	entries = append(entries, "config.yaml")

	trpcPath := filepath.Join(opts.ConfigDir, "trpc_go.yaml")
	if _, err := os.Stat(trpcPath); err == nil {
		if err := addFile(trpcPath, "trpc_go.yaml"); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
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

	sort.Strings(entries)
	return &BuildSCFPackageResult{Path: opts.OutPath, Entries: entries}, nil
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

func addZipBytes(zw *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{
		Name:   filepath.ToSlash(name),
		Method: zip.Deflate,
	}
	header.SetMode(0o644)
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func patchedConfigBytes(path, version string, overrides map[string]string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if root == nil {
		root = map[string]any{}
	}
	if version != "" {
		if err := setDottedYAMLValue(&root, "system.version", version); err != nil {
			return nil, err
		}
	}
	for key, value := range overrides {
		if strings.TrimSpace(key) == "" {
			continue
		}
		parsed, err := parseYAMLScalar(value)
		if err != nil {
			return nil, fmt.Errorf("override %s: %w", key, err)
		}
		if err := setDottedYAMLValue(&root, key, parsed); err != nil {
			return nil, err
		}
	}
	return yaml.Marshal(root)
}

func parseYAMLScalar(value string) (any, error) {
	var parsed any
	if err := yaml.Unmarshal([]byte(value), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func setDottedYAMLValue(root *any, dotted string, value any) error {
	parts := strings.Split(dotted, ".")
	if len(parts) == 0 {
		return fmt.Errorf("empty key")
	}
	current, ok := (*root).(map[string]any)
	if !ok {
		current = map[string]any{}
		*root = current
	}
	for _, part := range parts[:len(parts)-1] {
		if part == "" {
			return fmt.Errorf("invalid dotted key %q", dotted)
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	leaf := parts[len(parts)-1]
	if leaf == "" {
		return fmt.Errorf("invalid dotted key %q", dotted)
	}
	current[leaf] = value
	return nil
}
