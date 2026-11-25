package resample

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
	"github.com/stretchr/testify/assert"
)

func TestNewJobDefinitionRunsLocallyAndDeclaresSourceTask(t *testing.T) {
	definition := NewJobDefinition()
	assert.Equal(t, jobdef.ExecutionModeCollectorLocal, definition.ExecutionMode)

	keys := map[string]bool{}
	for _, field := range definition.Fields {
		keys[field.FieldKey] = field.IsRequired
	}
	assert.True(t, keys["source_task_id"], "合成任务由源采集任务创建")
	assert.True(t, keys["target_frequency"])
	assert.NotContains(t, keys, "settle_delay_ms", "事件触发后不再有收盘等待")
}
