package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestResampleStableTaskIDIncludesSourceSeriesTag(t *testing.T) {
	spec := TaskSpec{Provider: "moox", MarketType: "spot", DataType: "kline_resample", DatasetID: "derived", SubjectID: "BTC-USDT", Frequency: "4H"}
	left := StableResampleTaskID("crypto", "rule-1", spec, "venue:binance")
	right := StableResampleTaskID("crypto", "rule-1", spec, "venue:okx")
	assert.NotEqual(t, left, right)
	assert.Equal(t, left, StableResampleTaskID("crypto", "rule-1", spec, " venue:binance "))
}

func TestParseResampleTaskResultStrictAndNormalizesUTC(t *testing.T) {
	result, err := ParseResampleTaskResult(`{
		"schema_version":1,
		"state":"waiting_source",
		"state_version":3,
		"active_origin":"realtime",
		"active_bucket":"2026-08-29T08:00:00+08:00",
		"lease_until":null,
		"attempt":2,
		"next_retry_at":"2026-08-29T08:01:05+08:00",
		"realtime_next_bucket":"2026-08-29T00:00:00Z",
		"last_success_bucket":null,
		"last_input_hash":"",
		"last_error":"missing source row",
		"backfill":null
	}`)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), *result.ActiveBucket)
	assert.Equal(t, time.Date(2026, 8, 29, 0, 1, 5, 0, time.UTC), *result.NextRetryAt)

	encoded, err := result.Marshal()
	require.NoError(t, err)
	assert.Contains(t, encoded, `"active_bucket":"2026-08-29T00:00:00Z"`)
}

func TestParseResampleTaskResultRejectsUnknownAndInvalidState(t *testing.T) {
	_, err := ParseResampleTaskResult(`{"schema_version":1,"state":"idle","state_version":0,"unexpected":true}`)
	require.ErrorContains(t, err, "unknown field")

	_, err = ParseResampleTaskResult(`{"schema_version":1,"state":"bogus","state_version":0}`)
	require.ErrorContains(t, err, "invalid resample task state")

	_, err = ParseResampleTaskResult(`{"schema_version":2,"state":"idle","state_version":0}`)
	require.ErrorContains(t, err, "schema_version")

	_, err = ParseResampleTaskResult(`{"schema_version":1,"state":"idle","state_version":-1}`)
	require.ErrorContains(t, err, "state_version")
}

func TestResampleTaskResultRejectsEmptyBackfill(t *testing.T) {
	_, err := ParseResampleTaskResult(`{"schema_version":1,"state":"idle","state_version":0,"backfill":{}}`)
	require.ErrorContains(t, err, "backfill.request_id")
}
