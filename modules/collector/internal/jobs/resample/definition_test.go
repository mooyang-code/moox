package resample

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewJobDefinitionMatchesMooxSpotAndDeclaresSourceTask(t *testing.T) {
	definition := NewJobDefinition()
	params, err := domain.ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_spot_kline_derived_4h","target_frequency":"4h"}`, "", "", "kline_resample")
	require.NoError(t, err)
	require.True(t, definition.Matches(params))
	assert.Equal(t, jobdef.ExecutionModeCollectorLocal, definition.ExecutionMode)

	keys := map[string]bool{}
	for _, field := range definition.Fields {
		keys[field.FieldKey] = field.IsRequired
	}
	assert.True(t, keys["source_task_id"], "合成任务由源采集任务创建")
	assert.True(t, keys["target_frequency"])
	assert.NotContains(t, keys, "settle_delay_ms", "事件触发后不再有收盘等待")
}
