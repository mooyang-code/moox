package schema

import (
	"strings"
	"testing"
)

func TestAllSQLDropsLegacyInvocationTables(t *testing.T) {
	sql := AllSQL()
	for _, forbidden := range []string{
		"CREATE TABLE IF NOT EXISTS t_cloud_invocations",
		"CREATE TABLE IF NOT EXISTS t_cloud_invocation_results",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("cloudnode schema must not recreate %s", forbidden)
		}
	}
	for _, want := range []string{
		"DROP TABLE IF EXISTS t_cloud_invocation_results",
		"DROP TABLE IF EXISTS t_cloud_invocations",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("cloudnode schema must include %q", want)
		}
	}
}
