package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxFileBytes = 8 << 20

type workspace struct {
	root string
}

func newWorkspace(root string) (workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return workspace{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return workspace{}, err
	}
	if !info.IsDir() {
		return workspace{}, fmt.Errorf("%s is not a directory", abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return workspace{}, err
	}
	return workspace{root: filepath.Clean(resolved)}, nil
}

func (w workspace) resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(w.root, full)
	}
	full = filepath.Clean(full)
	if !w.contains(full) {
		return "", fmt.Errorf("path escapes project root")
	}
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		resolved = filepath.Clean(resolved)
		if !w.contains(resolved) {
			return "", fmt.Errorf("symlink escapes project root")
		}
		return resolved, nil
	}
	existing := full
	var suffix []string
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		if existing == w.root || existing == string(os.PathSeparator) {
			return "", fmt.Errorf("parent directory does not exist")
		}
		suffix = append([]string{filepath.Base(existing)}, suffix...)
		existing = filepath.Dir(existing)
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if !w.contains(resolved) {
		return "", fmt.Errorf("symlink escapes project root")
	}
	return filepath.Join(append([]string{resolved}, suffix...)...), nil
}

func (w workspace) contains(path string) bool {
	path = filepath.Clean(path)
	return path == w.root || strings.HasPrefix(path, w.root+string(os.PathSeparator))
}
