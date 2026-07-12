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
}
