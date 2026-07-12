package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTaskRule_TableName_ShouldReturnCollectorTaskRulesTable(t *testing.T) {
	assert.Equal(t, "t_collector_task_rules", (&TaskRule{}).TableName())
}
