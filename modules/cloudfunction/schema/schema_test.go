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
			t.Fatalf("cloudfunction schema must not recreate %s", forbidden)
		}
	}
}

func TestSchemaContainsFunctionJobTablesAndIndexes(t *testing.T) {
	sql := AllSQL()
	for _, required := range []string{
		"t_cloud_function_jobs",
		"t_cloud_function_job_items",
		"idx_t_cloud_function_jobs_space_job",
		"idx_t_cloud_function_job_items_job_index",
		"idx_t_cloud_function_job_items_status_id",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("cloudfunction schema does not contain %s", required)
		}
	}
}
