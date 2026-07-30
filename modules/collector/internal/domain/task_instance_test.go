package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTaskInstance_TableName_ShouldReturnCollectorTaskInstancesTable(t *testing.T) {
	assert.Equal(t, "t_collector_task_instances", (&TaskInstance{}).TableName())
}

func TestTaskInstanceStatusValuesAreCompact(t *testing.T) {
	assert.Equal(t, 1, InstanceStatusPending)
	assert.Equal(t, 2, InstanceStatusSuccess)
	assert.Equal(t, 3, InstanceStatusFailed)
}

func TestTaskInstance_StableTaskID_SameInput_ShouldReturnDeterministicID(t *testing.T) {
	spec := TaskSpec{
		Exchange:  "binance",
		Market:    "spot",
		DataType:  "kline",
		DatasetID: "spot_kline_1h",
		SubjectID: "btc-usdt",
		Interval:  "1m",
	}
	first := StableTaskID("crypto", "rule-1", spec)
	second := StableTaskID("crypto", "rule-1", spec)
	assert.Equal(t, first, second)
	assert.Len(t, first, 32)
}

func TestTaskInstance_StableTaskID_DifferentSpace_ShouldReturnDifferentID(t *testing.T) {
	spec := TaskSpec{
		Exchange:  "binance",
		Market:    "spot",
		DataType:  "kline",
		DatasetID: "spot_kline_1h",
		SubjectID: "btc-usdt",
		Interval:  "1m",
	}
	left := StableTaskID("crypto", "rule-1", spec)
	right := StableTaskID("equity", "rule-1", spec)
	assert.NotEqual(t, left, right)
}
