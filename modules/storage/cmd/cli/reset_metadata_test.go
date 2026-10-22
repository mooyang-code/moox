package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/bootstrap/metadata"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

const (
	testSchemaPath = "../../schema/metadata.sql"
	testSeedPath   = "../../../../config/setup/metadata.yaml"
)

func seededStorage(t *testing.T) (storageconfig.StorageConfig, *sql.DB) {
	t.Helper()
	storage := storageconfig.StorageConfig{Root: t.TempDir()}
	ctx := context.Background()
	require.NoError(t, metadata.InitSchema(ctx, metadata.SchemaOptions{Storage: storage, SchemaPath: testSchemaPath}))
	// 种子里的数据集指向已登记的 DataNode（线上由 register-node 登记）。
	node, err := sql.Open("sqlite", metadataDBPath(storage))
	require.NoError(t, err)
	_, err = node.Exec("INSERT INTO t_data_nodes (c_node_id, c_name, c_service_target) VALUES ('storage-node-0', 'storage-node-0', 'ip://127.0.0.1:1')")
	require.NoError(t, err)
	require.NoError(t, node.Close())
	_, err = metadata.ImportSeed(ctx, metadata.SeedOptions{Storage: storage, SchemaPath: testSchemaPath, SeedPath: testSeedPath})
	require.NoError(t, err)
	db, err := sql.Open("sqlite", metadataDBPath(storage)+"?_pragma=busy_timeout(10000)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return storage, db
}

func rowCount(t *testing.T, db *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

// 每张业务表都必须归类；schema 新增表而清单漏了会让这个测试失败。
func TestMetadataResetClassifiesEveryTable(t *testing.T) {
	_, db := seededStorage(t)
	require.NoError(t, checkMetadataTables(context.Background(), db))
	_, err := db.Exec("CREATE TABLE t_new_thing (c_id INTEGER)")
	require.NoError(t, err)
	require.ErrorContains(t, checkMetadataTables(context.Background(), db), "t_new_thing")
}

// 清空业务定义后重新导入种子，回到部署后的状态；DataNode、设备、修订号保留（修订号不回退）。
func TestMetadataResetRestoresSeedAndKeepsNodes(t *testing.T) {
	storage, db := seededStorage(t)
	ctx := context.Background()
	spacesBefore, viewsBefore := rowCount(t, db, "t_spaces"), rowCount(t, db, "t_views")
	require.Greater(t, spacesBefore, int64(0))
	// 种子之外用户加的空间：重置后应该消失。
	_, err := db.Exec("INSERT INTO t_spaces (c_space_id, c_name) VALUES ('custom', '自定义空间')")
	require.NoError(t, err)
	spacesBefore++
	revisionBefore := rowCount(t, db, "t_space_revisions")

	dry, err := resetMetadata(ctx, resetMetadataOptions{Storage: storage, SchemaPath: testSchemaPath, SeedPath: testSeedPath, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, "dry-run", dry.Status)
	require.Equal(t, spacesBefore, dry.Deleted["t_spaces"])
	require.Equal(t, spacesBefore, rowCount(t, db, "t_spaces"), "dry-run 不改动")

	res, err := resetMetadata(ctx, resetMetadataOptions{Storage: storage, SchemaPath: testSchemaPath, SeedPath: testSeedPath})
	require.NoError(t, err)
	require.Equal(t, "ok", res.Status)
	require.FileExists(t, res.Backup)
	require.Equal(t, spacesBefore-1, rowCount(t, db, "t_spaces"), "自定义空间被清掉，种子空间回来")
	require.Equal(t, viewsBefore, rowCount(t, db, "t_views"))
	require.EqualValues(t, 1, rowCount(t, db, "t_data_nodes"), "DataNode 登记保留")
	require.GreaterOrEqual(t, rowCount(t, db, "t_space_revisions"), revisionBefore)
	require.NotNil(t, res.Imported)

	entries, err := os.ReadDir(filepath.Join(filepath.Dir(metadataDBPath(storage)), "reset-backups"))
	require.NoError(t, err)
	require.NotEmpty(t, entries)
}

func TestMetadataResetRequiresConfirmationAndSeed(t *testing.T) {
	require.ErrorContains(t, runResetMetadata([]string{"--seed", testSeedPath}, nil, nil), "--yes")
}
