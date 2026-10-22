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
	assert.True(t, strings.Contains(sql, "t_collector_tasks"))
	assert.NotContains(t, sql, "c_planned_exec_node")
	assert.NotContains(t, sql, "c_last_exec_node")
	assert.NotContains(t, sql, "DROP TABLE IF EXISTS t_collector_execution_logs")
	assert.NotContains(t, sql, "CREATE TABLE IF NOT EXISTS t_collector_task_instances")
	assert.Contains(t, sql, "CREATE TABLE IF NOT EXISTS t_collector_resample_jobs")
	assert.Contains(t, sql, "DROP TABLE IF EXISTS t_collector_task_instances")
}
