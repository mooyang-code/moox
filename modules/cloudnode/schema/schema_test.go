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
