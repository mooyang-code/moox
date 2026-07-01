package schema_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStorageMetadataSchemaMatchesCurrentConcepts(t *testing.T) {
	schema := readStorageMetadataSchema(t)

	for _, table := range []string{
		"t_spaces",
		"t_views",
		"t_view_columns",
		"t_data_sources",
		"t_subjects",
		"t_subject_symbols",
		"t_datasets",
		"t_dataset_subjects",
		"t_dataset_columns",
		"t_fields",
		"t_factors",
		"t_primary_store_nodes",
		"t_storage_devices",
		"t_primary_store_routes",
		"t_archive_files",
	} {
		require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS "+table, table)
	}

	for _, table := range []string{
		"t_views",
		"t_view_columns",
		"t_data_sources",
		"t_subjects",
		"t_subject_symbols",
		"t_datasets",
		"t_dataset_subjects",
		"t_dataset_columns",
		"t_fields",
		"t_factors",
		"t_primary_store_routes",
		"t_archive_files",
	} {
		requireTableContains(t, schema, table, "c_space_id TEXT NOT NULL")
	}

	requireTableContains(t, schema, "t_views", "c_primary_dataset_id TEXT NOT NULL")
	requireTableContains(t, schema, "t_views", "c_active_result TEXT NOT NULL")
	requireTableContains(t, schema, "t_views", "c_view_version INTEGER NOT NULL DEFAULT 1")
	requireTableContains(t, schema, "t_views", "c_active_view_version INTEGER NOT NULL DEFAULT 0")
	requireTableContains(t, schema, "t_views", "c_building_view_version INTEGER NOT NULL DEFAULT 0")
	requireTableContains(t, schema, "t_views", "c_building_result TEXT NOT NULL DEFAULT ''")
	requireTableContains(t, schema, "t_views", "c_build_error TEXT NOT NULL DEFAULT ''")
	requireTableContains(t, schema, "t_views", "c_build_started_at TEXT NOT NULL DEFAULT ''")
	requireTableContains(t, schema, "t_views", "c_build_finished_at TEXT NOT NULL DEFAULT ''")
	requireTableContains(t, schema, "t_datasets", "c_data_source_id TEXT NOT NULL")
	requireTableContains(t, schema, "t_data_sources", "c_kind TEXT NOT NULL")
	requireTableContains(t, schema, "t_subject_symbols", "c_external_symbol TEXT NOT NULL")
	requireTableContains(t, schema, "t_view_columns", "c_origin_type TEXT NOT NULL")
	requireTableContains(t, schema, "t_view_columns", "c_origin_id TEXT NOT NULL")
	requireTableContains(t, schema, "t_dataset_columns", "c_origin_type TEXT NOT NULL")
	requireTableContains(t, schema, "t_dataset_columns", "c_origin_id TEXT NOT NULL")

	requireTableNotContains(t, schema, "t_subjects", "c_data_source_id")
	requireTableNotContains(t, schema, "t_subjects", "c_source_symbol")
	requireTableNotContains(t, schema, "t_subjects", "c_aliases_json")
	requireTableNotContains(t, schema, "t_views", "c_freq")
	requireTableNotContains(t, schema, "t_views", "c_active_table")
	requireTableNotContains(t, schema, "t_subject_symbols", "c_aliases_json")
	requireTableNotContains(t, schema, "t_view_columns", "c_source_type")
	requireTableNotContains(t, schema, "t_view_columns", "c_source_id")
	requireTableNotContains(t, schema, "t_dataset_columns", "c_source_type")
	requireTableNotContains(t, schema, "t_dataset_columns", "c_source_id")
	requireTableNotContains(t, schema, "t_fields", "c_interface_name")
	requireTableContains(t, schema, "t_storage_devices", "c_node_id TEXT NOT NULL")
	requireTableContains(t, schema, "t_primary_store_routes", "c_node_id TEXT NOT NULL")
	requireTableNotContains(t, schema, "t_primary_store_nodes", "c_role")
	requireTableNotContains(t, schema, "t_storage_devices", "c_entity_id")
	requireTableNotContains(t, schema, "t_primary_store_routes", "c_entity_id")
	requireTableNotContains(t, schema, "t_primary_store_routes", "c_device_id")
	require.NotContains(t, schema, "CREATE TABLE IF NOT EXISTS t_storage_entities")
	require.NotContains(t, schema, "CREATE TABLE IF NOT EXISTS t_subject_aliases")
	require.NotContains(t, schema, "CREATE TABLE IF NOT EXISTS t_space_views")
	require.NotContains(t, schema, "idx_t_primary_store_routes_device")
	oldStoragePrefix := "t_storage_"
	require.NotContains(t, schema, "CREATE TABLE IF NOT EXISTS "+oldStoragePrefix+"nodes")
	require.NotContains(t, schema, "CREATE TABLE IF NOT EXISTS "+oldStoragePrefix+"routes")
	require.NotContains(t, schema, "'"+"ob"+"ject"+"'")
}

func TestSQLTableDefinitionsLiveUnderStorageSchema(t *testing.T) {
	storageRoot := moduleRoot(t)
	repoRoot := filepath.Dir(filepath.Dir(storageRoot))
	allowed := map[string]bool{
		"modules/admin/schema/admin.sql":      true,
		"modules/storage/schema/metadata.sql": true,
	}
	storageTables := []string{
		"t_views",
		"t_view_columns",
		"t_data_sources",
		"t_subjects",
		"t_subject_symbols",
		"t_datasets",
		"t_dataset_subjects",
		"t_dataset_columns",
		"t_fields",
		"t_factors",
		"t_primary_store_nodes",
		"t_storage_devices",
		"t_primary_store_routes",
		"t_archive_files",
	}
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, err error) error {
		require.NoError(t, err)
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "bin", "data", "dist", "log", "logs", "release", "var":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".sql" {
			return nil
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		sql := strings.ToUpper(string(data))
		hasStorageTableDefinition := false
		for _, table := range storageTables {
			if strings.Contains(sql, strings.ToUpper("CREATE TABLE IF NOT EXISTS "+table)) {
				hasStorageTableDefinition = true
				break
			}
		}
		if !hasStorageTableDefinition {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		require.NoError(t, err)
		require.True(t, allowed[filepath.ToSlash(rel)], rel)
		return nil
	})
	require.NoError(t, err)
}

func readStorageMetadataSchema(t *testing.T) string {
	t.Helper()
	root := moduleRoot(t)
	require.NoFileExists(t, filepath.Join(root, "schema", "storage_metadata.sql"))
	require.NoFileExists(t, filepath.Join(root, "schema", "admin_console.sql"))
	data, err := os.ReadFile(filepath.Join(root, "schema", "metadata.sql"))
	require.NoError(t, err)
	schema := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, table := range []string{
		"t_users",
		"t_cloud_nodes",
		"t_ssh_host",
		"t_collector_task_rules",
	} {
		require.NotContains(t, schema, "CREATE TABLE IF NOT EXISTS "+table)
	}
	return schema
}

func requireTableContains(t *testing.T, schema, table, needle string) {
	t.Helper()
	require.Contains(t, tableBlock(t, schema, table), needle, table)
}

func requireTableNotContains(t *testing.T, schema, table, needle string) {
	t.Helper()
	require.NotContains(t, tableBlock(t, schema, table), needle, table)
}

func tableBlock(t *testing.T, schema, table string) string {
	t.Helper()
	startNeedle := "CREATE TABLE IF NOT EXISTS " + table
	start := strings.Index(schema, startNeedle)
	require.NotEqual(t, -1, start, table)
	rest := schema[start:]
	end := strings.Index(rest, ");")
	require.NotEqual(t, -1, end, table)
	return rest[:end+2]
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		require.NotEqual(t, wd, parent, "go.mod not found")
		wd = parent
	}
}
