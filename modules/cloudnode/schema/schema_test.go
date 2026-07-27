package schema

import (
	"strings"
	"testing"
)

func TestAllSQLDoesNotContainDeletedInvocationTables(t *testing.T) {
	sql := AllSQL()
	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS t_cloud_invocations",
		"CREATE TABLE IF NOT EXISTS t_cloud_invocation_results",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("cloudnode schema must not recreate %s", forbidden)
		}
	}
}

func TestAllSQLDoesNotContainDeletedOnlineJobItemTables(t *testing.T) {
	sql := AllSQL()
	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS t_cloud_job_items",
		"CREATE TABLE IF NOT EXISTS t_cloud_job_item_attempts",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("cloudnode schema must not recreate %s", forbidden)
		}
	}
}

func TestSchemaContainsNodeBatchTablesAndIndexes(t *testing.T) {
	sql := AllSQL()
	for _, required := range []string{
		"t_cloud_node_batches",
		"t_cloud_node_batch_items",
		"idx_cloud_node_batches_space_job",
		"idx_cloud_node_batch_items_job_index",
		"idx_cloud_node_batch_items_status_id",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("cloudnode schema does not contain %s", required)
		}
	}
}
