package collectorpackager

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// BuildSCFPackageOptions configures a Tencent SCF package build.
type BuildSCFPackageOptions struct {
	BinaryPath string
	ConfigDir  string
	OutPath    string
}

// BuildSCFPackageResult describes the created package.
type BuildSCFPackageResult struct {
	Path    string
	Entries []string
}

// BuildSCFPackage creates a zip containing main, config.yaml, trpc_go.yaml,
// and nested sources/ entries. When example_trpc_go.yaml is present, it is
// packaged as trpc_go.yaml so developer-local ignored configs do not leak into
// SCF packages.
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
	if err := addFile(configPath, "config.yaml"); err != nil {
		return nil, err
	}

	trpcPath := filepath.Join(opts.ConfigDir, "example_trpc_go.yaml")
	if _, err := os.Stat(trpcPath); err == nil {
		if err := addFile(trpcPath, "trpc_go.yaml"); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		trpcPath = filepath.Join(opts.ConfigDir, "trpc_go.yaml")
		if _, err := os.Stat(trpcPath); err == nil {
			if err := addFile(trpcPath, "trpc_go.yaml"); err != nil {
				return nil, err
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
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
