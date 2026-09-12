package inputcache

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

type CapacityResult struct {
	Bytes       int64
	Rebuilt     int
	PauseWrites bool
}

// DirectoryBytes includes active, retired, WAL and temporary files. Symlinks
// are rejected rather than following paths outside the disposable cache root.
func DirectoryBytes(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("cache contains symlink %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cache contains non-regular file %q", path)
		}
		if info.Size() > int64(^uint64(0)>>1)-size {
			return fmt.Errorf("cache size overflow")
		}
		size += info.Size()
		return nil
	})
	return size, err
}

func availableBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// MaintainCapacity is called by the engine timer with its current generations.
// PauseWrites must be honored by the read-through layer, which can still return
// source data without persisting it when the byte budget cannot be restored.
func MaintainCapacity(ctx context.Context, cfg Config, generations []*Generation) (CapacityResult, error) {
	return maintainCapacity(ctx, cfg, generations, availableBytes)
}

func maintainCapacity(ctx context.Context, cfg Config, generations []*Generation, free func(string) (uint64, error)) (result CapacityResult, err error) {
	result.PauseWrites = true
	if err = cfg.Validate(); err != nil {
		return result, err
	}
	if !cfg.Enabled {
		return result, nil
	}
	result.Bytes, err = DirectoryBytes(cfg.Dir)
	if err != nil {
		return result, err
	}
	type candidate struct {
		generation *Generation
		bytes      int64
	}
	candidates := []candidate{}
	for _, generation := range generations {
		if generation == nil {
			return result, fmt.Errorf("nil cache generation")
		}
		candidateErr := generation.Use(func(db *Database, _ uint64) error {
			if generation.compacted && generation.compactedAt == db.mutations.Load() {
				return nil
			}
			bytes, err := DirectoryBytes(generation.dir)
			if err != nil {
				return err
			}
			candidates = append(candidates, candidate{generation, bytes})
			return nil
		})
		if candidateErr != nil {
			return result, candidateErr
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].bytes > candidates[j].bytes })
	for _, candidate := range candidates {
		if result.Bytes <= cfg.MaxBytes {
			break
		}
		if err = ctx.Err(); err != nil {
			return result, err
		}
		remaining, freeErr := free(cfg.Dir)
		if freeErr != nil {
			return result, freeErr
		}
		// Reserve a full current-cache copy plus operational headroom. This is
		// conservative; the SQL working set still needs monitoring at runtime.
		if remaining < uint64(cfg.MinFreeBytes) || remaining-uint64(cfg.MinFreeBytes) < uint64(result.Bytes) {
			return result, fmt.Errorf("insufficient disk headroom for cache rebuild")
		}
		if err = candidate.generation.Rebuild(ctx, cfg.RebuildKeepRows); err != nil {
			return result, err
		}
		result.Rebuilt++
		result.Bytes, err = DirectoryBytes(cfg.Dir)
		if err != nil {
			return result, err
		}
	}
	result.PauseWrites = result.Bytes > cfg.MaxBytes
	return result, nil
}
