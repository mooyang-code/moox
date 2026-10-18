package unitpackage

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
)

type ExtractOptions struct {
	Archive        string
	Destination    string
	ExpectedSHA256 string
	Profile        string
	GOOS           string
	GOARCH         string
}

type Extraction struct {
	Directory string `json:"directory"`
	Package   Result `json:"package"`
}

// Extract publishes immutable software into a new directory. It never runs
// executables, reads operator configuration, writes current, or copies data or
// credentials. Callers render private configuration before activation.
// A nonempty result on error means publication succeeded but syncing the
// parent directory failed; callers must inspect that directory before retrying.
func Extract(ctx context.Context, options ExtractOptions) (result Extraction, returnErr error) {
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if _, err := Components(options.Profile); err != nil {
		return result, err
	}
	if err := unitPlatform(options.GOOS, options.GOARCH); err != nil {
		return result, err
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(options.ExpectedSHA256, "sha256:"))
	if err != nil || len(decoded) != 32 || !strings.HasPrefix(options.ExpectedSHA256, "sha256:") || strings.ToLower(options.ExpectedSHA256) != options.ExpectedSHA256 {
		return result, errors.New("extraction requires the expected lowercase sha256 digest from the package producer")
	}
	destination := options.Destination
	if destination == "" || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || strings.ContainsAny(destination, "\x00\r\n") || destination == string(filepath.Separator) {
		return result, errors.New("extraction destination must be a new absolute clean directory")
	}
	parentPath := filepath.Dir(destination)
	physical, err := filepath.EvalSymlinks(parentPath)
	if err != nil || physical != parentPath {
		return result, errors.New("extraction parent must be an existing physical directory")
	}
	info, err := os.Lstat(parentPath)
	if err != nil || !info.IsDir() || !fsutil.Owned(info) || info.Mode().Perm()&0o022 != 0 {
		return result, errors.New("extraction parent must be owned and not writable by other users")
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return result, err
	}
	defer parent.Close()
	opened, err := parent.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return result, errors.New("extraction parent changed while opening")
	}
	name := filepath.Base(destination)
	if _, err := parent.Lstat(name); !os.IsNotExist(err) {
		return result, errors.New("extraction never replaces an existing destination")
	}
	stageName := ".extract-" + rand.Text()
	if err := parent.Mkdir(stageName, 0o700); err != nil {
		return result, err
	}
	defer func() {
		if err := parent.RemoveAll(stageName); err != nil {
			returnErr = errors.Join(returnErr, errors.New("could not remove unpublished extraction directory"))
		}
	}()
	stage, err := parent.OpenRoot(stageName)
	if err != nil {
		return result, err
	}
	defer stage.Close()
	directories := map[string]bool{".": true}
	verified, err := inspect(ctx, options.Archive, func(header *tar.Header) (*os.File, error) {
		directory := path.Dir(header.Name)
		if !directories[directory] {
			if err := stage.MkdirAll(filepath.FromSlash(directory), 0o700); err != nil {
				return nil, err
			}
			for dir := directory; dir != "."; dir = path.Dir(dir) {
				directories[dir] = true
			}
		}
		// os.Root confines resolution, and O_EXCL refuses duplicate paths,
		// links or file/directory collisions even before manifest validation.
		return stage.OpenFile(filepath.FromSlash(header.Name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	})
	if err != nil {
		return result, err
	}
	if verified.SHA256 != options.ExpectedSHA256 || verified.Manifest.Profile != options.Profile || verified.Manifest.GOOS != options.GOOS || verified.Manifest.GOARCH != options.GOARCH {
		return result, errors.New("deployment archive does not match its expected digest, profile or platform")
	}
	// Sync every created directory, including parents of nested assets. Files
	// were synced by the same scanner that computed their verified digests.
	for directory := range directories {
		file, err := stage.Open(filepath.FromSlash(directory))
		if err != nil {
			return result, err
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	directory, err := parent.Open(".")
	if err != nil {
		return result, err
	}
	defer directory.Close()
	if err := fsutil.RenameExclusive(directory, stageName, name); err != nil {
		return result, fmt.Errorf("publish deployment directory without replacement: %w", err)
	}
	result = Extraction{Directory: destination, Package: verified}
	if err := directory.Sync(); err != nil {
		return result, fmt.Errorf("deployment directory published but parent sync failed: %w", err)
	}
	return result, nil
}
