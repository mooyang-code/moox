package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"golang.org/x/sys/unix"
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
	if err := os.MkdirAll(a.FactorsDir, 0o755); err != nil {
		return "", fmt.Errorf("create factor artifacts root: %w", err)
	}
	rootFD, err := unix.Open(a.FactorsDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("open factor artifacts root without following symlinks: %w", err)
	}
	defer unix.Close(rootFD)
	if err := unix.Mkdirat(rootFD, factor.Name, 0o755); err != nil {
		if err != unix.EEXIST {
			return "", fmt.Errorf("create factor artifact directory: %w", err)
		}
	} else if err := unix.Fsync(rootFD); err != nil {
		return "", fmt.Errorf("sync factor artifacts root: %w", err)
	}
	dirFD, err := unix.Openat(rootFD, factor.Name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", fmt.Errorf("open factor artifact directory without following symlinks: %w", err)
	}
	defer unix.Close(dirFD)

	targetName := factor.SourceHash + ".py"
	path := filepath.Join(a.FactorsDir, factor.Name, targetName)
	if err := validateExistingArtifact(dirFD, targetName, factor.SourceCode, path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	tmpName, tmpFD, err := createArtifactTemp(dirFD)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Unlinkat(dirFD, tmpName, 0) }()
	tmp := os.NewFile(uintptr(tmpFD), tmpName)
	if _, err := io.WriteString(tmp, factor.SourceCode); err != nil {
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
	if err := unix.Linkat(dirFD, tmpName, dirFD, targetName, 0); err != nil {
		if err == unix.EEXIST {
			if validateErr := validateExistingArtifact(dirFD, targetName, factor.SourceCode, path); validateErr == nil {
				return path, nil
			} else {
				return "", validateErr
			}
		}
		return "", fmt.Errorf("publish factor artifact without replacement: %w", err)
	}
	if err := unix.Fsync(dirFD); err != nil {
		return "", fmt.Errorf("sync factor artifact directory: %w", err)
	}
	return path, nil
}

func createArtifactTemp(dirFD int) (string, int, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var random [12]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", -1, fmt.Errorf("generate temporary artifact name: %w", err)
		}
		name := ".factor-" + hex.EncodeToString(random[:]) + ".tmp"
		fd, err := unix.Openat(dirFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
		if err == nil {
			return name, fd, nil
		}
		if err != unix.EEXIST {
			return "", -1, fmt.Errorf("create temporary factor artifact: %w", err)
		}
	}
	return "", -1, fmt.Errorf("create temporary factor artifact: too many name collisions")
}

func validateExistingArtifact(dirFD int, name, sourceCode, path string) error {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if err == unix.ENOENT {
			return os.ErrNotExist
		}
		return fmt.Errorf("open existing factor artifact without following symlinks: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect existing factor artifact: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("factor artifact %q is not a regular file", path)
	}
	current, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read existing factor artifact: %w", err)
	}
	if string(current) != sourceCode {
		return fmt.Errorf("existing factor artifact %q does not match its source hash", path)
	}
	return nil
}
