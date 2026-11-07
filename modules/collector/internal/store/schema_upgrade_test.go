package store

import (
	"os"
	"path/filepath"
	"testing"

	schema "github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

// 旧库（统一数据集之前的表结构）不能静默启动：新增的列和重建的表缺失时拒绝，并指向迁移脚本；
// 按迁移脚本升级后通过，已有的数据保留。
func TestApplySchemaRejectsUnupgradedDatabaseAndMigrationFixesIt(t *testing.T) {
	old, err := os.ReadFile(filepath.Join("testdata", "collector-before-unified.sql"))
	require.NoError(t, err)
	migration, err := os.ReadFile(filepath.Join("..", "..", "schema", "migrations", "20261105-unified-dataset.sql"))
	require.NoError(t, err)

	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.db.Exec(string(old)).Error)
	require.NoError(t, db.db.Exec(`INSERT INTO t_collector_source_absences (c_space_id, c_dataset_id, c_frequency, c_subject_id, c_from, c_to, c_bars)
		VALUES ('crypto', 'ds', '1m', 'BTC-USDT', '2026-11-01 00:00:00', '2026-11-01 00:05:00', 5)`).Error)

	err = db.ApplySchema(schema.AllSQL())
	require.ErrorIs(t, err, ErrSchemaNotUpgraded)
	require.Contains(t, err.Error(), "20261105-unified-dataset.sql")
	require.Contains(t, err.Error(), "c_series_tag")

	require.NoError(t, db.db.Exec(string(migration)).Error)
	require.NoError(t, db.ApplySchema(schema.AllSQL()), "升级后再启动检查通过")
	var rows int64
	require.NoError(t, db.db.Raw(`SELECT COUNT(*) FROM t_collector_source_absences WHERE c_subject_id = 'BTC-USDT' AND c_series_tag = '' AND c_bars = 5`).Scan(&rows).Error)
	require.Equal(t, int64(1), rows, "重建表时保留已有数据")
}

// 全新的库直接通过检查。
func TestApplySchemaAcceptsFreshDatabase(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
}
