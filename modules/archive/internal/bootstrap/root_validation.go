package bootstrap

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/parquetio"
)

func validateArchiveRoot(ctx context.Context, root string, store *journal.Store) error {
	return walkArchiveFiles(ctx, root, func(path string) error {
		key, err := domain.ParseArchivePath(path)
		if err != nil {
			return err
		}
		_, _, metadata, err := parquetio.Read(path)
		if err != nil {
			return err
		}
		generation, err := strconv.ParseUint(metadata["moox.archive.generation"], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid archive generation metadata: %w", err)
		}
		manifest, err := parquetio.Validate(path, key, generation)
		if err != nil {
			return err
		}
		state, err := store.PartitionState(ctx, key)
		if err != nil {
			return fmt.Errorf("archive file has no journal manifest: %w", err)
		}
		if state.Manifest == nil || state.Manifest.Generation != manifest.Generation ||
			state.Manifest.SHA256 != manifest.SHA256 || state.Manifest.Size != manifest.Size ||
			state.Manifest.RowCount != manifest.RowCount {
			return fmt.Errorf("archive file does not match journal manifest")
		}
		return nil
	})
}

func validateArchivePaths(ctx context.Context, root string) error {
	return walkArchiveFiles(ctx, root, func(path string) error {
		_, err := domain.ParseArchivePath(path)
		return err
	})
}

func walkArchiveFiles(ctx context.Context, root string, check func(string) error) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".parquet") {
			return nil
		}
		return check(path)
	})
}
