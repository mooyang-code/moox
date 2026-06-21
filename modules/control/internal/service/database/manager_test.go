package database

import (
	"testing"

	"github.com/mooyang-code/moox/modules/control/internal/config"
	"github.com/stretchr/testify/require"
)

func TestInitializeAppliesAdminSchema(t *testing.T) {
	manager := NewManager()
	err := manager.Initialize(&config.DatabaseConfig{Path: t.TempDir() + "/moox.db"})
	require.NoError(t, err)

	db := manager.GetDB()
	require.NotNil(t, db)

	for _, table := range []string{"t_spaces", "t_space_members", "t_users", "t_cloud_nodes"} {
		var count int64
		err = db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count).Error
		require.NoError(t, err)
		require.Equal(t, int64(1), count, table)
	}
}

func TestEmbeddedAdminSchemaContainsCoreTables(t *testing.T) {
	schema := adminSchemaSQL()
	require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_spaces")
	require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_users")
	require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS t_cloud_nodes")
}
