package metadata

import (
	"context"
	"os"
	"path/filepath"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
)

// SchemaOptions 保存元数据表初始化所需的配置。
type SchemaOptions struct {
	Storage storageconfig.StorageConfig
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
	pebbleBase := opts.Storage.Devices.PebblePath
	if pebbleBase == "" {
		pebbleBase = filepath.Join(root, "pebble")
	}
	pebbleDir := filepath.Join(pebbleBase, "storage-primary")
	hadData := nonEmptyFile(metadataPath) || fileExists(filepath.Join(pebbleDir, "CURRENT"))

	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o755); err != nil {
		return err
	}
	store, err := metasqlite.Open(ctx, metasqlite.Options{Path: metadataPath})
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.InitSchema(ctx); err != nil {
		return err
	}
	// Pebble 尚无数据或布局版本已匹配时核对并写出 storage-primary 的格式文件；
	// 若已有旧版 Pebble 数据则不覆盖格式文件，交由主服务启动时拒绝。
	if ok, err := pebble.IsCompatibleOrEmpty(pebbleDir); err == nil && ok {
		if err := metasqlite.RecordPrimaryFormat(root, hadData); err != nil {
			return err
		}
	}
	return nil
}

func nonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
