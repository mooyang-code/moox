package moduleregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var ErrInvalidLogicalID = errors.New("pyruntime: invalid logical id")

type ModuleSource struct {
	Type, LogicalID string
	Source          []byte
}
type ModuleVersion struct{ Type, LogicalID, SourceHash, Path string }
type SourcePublisher struct{ root string }

var safeID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func NewSourcePublisher(root string) *SourcePublisher { return &SourcePublisher{root: root} }
func (p *SourcePublisher) Publish(ctx context.Context, src ModuleSource) (ModuleVersion, error) {
	if err := ctx.Err(); err != nil {
		return ModuleVersion{}, err
	}
	if !safeID.MatchString(src.Type) || !safeID.MatchString(src.LogicalID) || src.Type == "." || src.Type == ".." || src.LogicalID == "." || src.LogicalID == ".." {
		return ModuleVersion{}, ErrInvalidLogicalID
	}
	if len(src.Source) == 0 {
		return ModuleVersion{}, errors.New("pyruntime: source is empty")
	}
	sum := sha256.Sum256(src.Source)
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(p.root, src.Type, src.LogicalID, hash)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ModuleVersion{}, err
	}
	path := filepath.Join(dir, "module.py")
	if _, err := os.Stat(path); err == nil {
		return ModuleVersion{src.Type, src.LogicalID, hash, path}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ModuleVersion{}, err
	}
	tmp, err := os.CreateTemp(dir, ".module-")
	if err != nil {
		return ModuleVersion{}, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err = tmp.Write(src.Source); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return ModuleVersion{}, err
	}
	if err = os.Rename(tmpPath, path); err != nil && !errors.Is(err, os.ErrExist) {
		return ModuleVersion{}, fmt.Errorf("publish module: %w", err)
	}
	return ModuleVersion{src.Type, src.LogicalID, hash, path}, nil
}
