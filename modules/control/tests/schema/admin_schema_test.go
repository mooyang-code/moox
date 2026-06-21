package schema_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdminSchemaOwnsControlTables(t *testing.T) {
	schema := readAdminSchema(t)

	for _, table := range []string{
		"t_spaces",
		"t_space_members",
		"t_users",
		"t_active_tokens",
		"t_login_history",
		"t_user_actions",
		"t_cloud_accounts",
		"t_cloud_nodes",
		"t_function_packages",
		"t_collector_data_type_configs",
		"t_collector_field_configs",
		"t_collector_task_rules",
		"t_collector_task_instances",
		"t_async_jobs",
		"t_async_job_tasks",
		"t_node_task_snapshot",
		"t_exchange_symbols",
		"t_ssh_host",
		"t_ssh_session",
		"t_host_monitor_history",
	} {
		require.Contains(t, schema, "CREATE TABLE IF NOT EXISTS "+table, table)
	}

	for _, table := range []string{
		"t_cloud_accounts",
		"t_cloud_nodes",
		"t_function_packages",
		"t_collector_task_rules",
		"t_collector_task_instances",
		"t_async_jobs",
		"t_async_job_tasks",
		"t_node_task_snapshot",
		"t_exchange_symbols",
		"t_ssh_host",
		"t_ssh_session",
		"t_host_monitor_history",
	} {
		requireTableContains(t, schema, table, "c_space_id TEXT NOT NULL")
	}

	for _, table := range []string{
		"t_users",
		"t_active_tokens",
		"t_login_history",
		"t_user_actions",
	} {
		requireTableNotContains(t, schema, table, "c_space_id")
	}

	require.NotContains(t, schema, "CREATE TABLE IF NOT EXISTS t_datasets")
	require.NotContains(t, schema, "CREATE TABLE IF NOT EXISTS t_views")
	require.NotContains(t, schema, "migrations/")
}

func TestOnlyExpectedSQLSchemaFilesExist(t *testing.T) {
	repoRoot := filepath.Dir(filepath.Dir(moduleRoot(t)))
	var found []string
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
		rel, err := filepath.Rel(repoRoot, path)
		require.NoError(t, err)
		found = append(found, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		"modules/control/schema/admin.sql",
		"modules/storage/schema/metadata.sql",
	}, found)
}

func readAdminSchema(t *testing.T) string {
	t.Helper()
	root := moduleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "schema", "admin.sql"))
	require.NoError(t, err)
	return strings.ReplaceAll(string(data), "\r\n", "\n")
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
