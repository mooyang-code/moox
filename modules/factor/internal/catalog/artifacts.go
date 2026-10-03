package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

var artifactName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Artifacts materializes immutable Python source files consumed by workers.
type Artifacts struct {
	FactorsDir string
}

// Materialize writes one source version atomically and never replaces a
// different file already stored under the same content hash.
func (a Artifacts) Materialize(factor domain.FactorDef) (string, error) {
	if a.FactorsDir == "" {
		return "", fmt.Errorf("factors directory is required")
	}
	if !artifactName.MatchString(factor.Name) {
		return "", fmt.Errorf("factor name %q is not a valid source directory", factor.Name)
	}
	if factor.SourceHash == "" || factor.SourceHash != domain.SourceHash(factor.SourceCode) {
		return "", fmt.Errorf("factor source hash does not match source code")
	}
	dir := filepath.Join(a.FactorsDir, factor.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create factor artifact directory: %w", err)
	}
	path := filepath.Join(dir, factor.SourceHash+".py")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("factor artifact %q is not a regular file", path)
		}
		current, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", fmt.Errorf("read existing factor artifact: %w", readErr)
		}
		if string(current) != factor.SourceCode {
			return "", fmt.Errorf("existing factor artifact %q does not match its source hash", path)
		}
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect factor artifact: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".factor-*.py")
	if err != nil {
		return "", fmt.Errorf("create temporary factor artifact: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("set factor artifact permissions: %w", err)
	}
	if _, err := tmp.WriteString(factor.SourceCode); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write factor artifact: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("sync factor artifact: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close factor artifact: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() {
			current, readErr := os.ReadFile(path)
			if readErr == nil && string(current) == factor.SourceCode {
				return path, nil
			}
		}
		return "", fmt.Errorf("publish factor artifact: %w", err)
	}
	return path, nil
}
