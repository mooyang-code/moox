package schema

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllSQL_ShouldReturnNonEmptySchema(t *testing.T) {
	sql := AllSQL()
	require.NotEmpty(t, sql)
	assert.Contains(t, sql, "CREATE TABLE")
	assert.True(t, strings.Contains(sql, "t_collector_task_rules"))
	assert.NotContains(t, sql, "c_planned_exec_node")
	assert.NotContains(t, sql, "c_last_exec_node")
	assert.NotContains(t, sql, "DROP TABLE IF EXISTS t_collector_execution_logs")
	assert.Contains(t, sql, "idx_collector_instances_exec ON t_collector_task_instances (c_last_exec_status)")
	assert.Contains(t, sql, "c_coverage_start_time DATETIME")
}
