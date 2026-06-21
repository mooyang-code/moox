package metadata

import (
	"context"
	"os"
	"path/filepath"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
)

// SchemaOptions 保存元数据表初始化所需的路径与开关。
type SchemaOptions struct {
	Storage    storageconfig.StorageConfig
	SchemaPath string
}

func InitSchema(ctx context.Context, opts SchemaOptions) error {
	root := opts.Storage.Root
	if root == "" {
		root = "var/storage"
	}
	metadataPath := opts.Storage.Metadata.Path
	if metadataPath == "" {
		metadataPath = filepath.Join(root, "metadata", "storage_metadata.db")
	}
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o755); err != nil {
		return err
	}
	store, err := metasqlite.Open(ctx, metasqlite.Options{Path: metadataPath, SchemaPath: opts.SchemaPath})
	if err != nil {
		return err
	}
	defer store.Close()
	return store.InitSchema(ctx)
}
