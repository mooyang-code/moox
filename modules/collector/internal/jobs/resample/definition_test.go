package resample

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs/jobdef"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewJobDefinitionMatchesMooxSpotAndBuildsLocalSpecs(t *testing.T) {
	definition := NewJobDefinition()
	params, err := domain.ParseCollectParams(`{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"spot_kline_derived_4h","target_frequency":"4H","alignment":"epoch_utc"}`, "", "", "kline_resample")
	require.NoError(t, err)
	require.True(t, definition.Matches(params))
	assert.Equal(t, jobdef.ExecutionModeCollectorLocal, definition.ExecutionMode)

	specs, err := definition.Planner(context.Background(), &domain.TaskRule{RuleID: "rule-1"}, params, []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}})
	require.NoError(t, err)
	require.Len(t, specs, 1)
	assert.Equal(t, "spot_kline_derived_4h", specs[0].DatasetID)
	assert.Equal(t, "4H", specs[0].Frequency)
	assert.Equal(t, "BTC-USDT", specs[0].SubjectID)
}
