package rpc

import (
	"testing"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricPageAndResult(t *testing.T) {
	offset, limit := metricPage(nil)
	assert.Equal(t, 0, offset)
	assert.Equal(t, 50, limit)
	offset, limit = metricPage(&commonpb.Page{Page: 2, Size: 1000})
	assert.Equal(t, 500, offset)
	assert.Equal(t, 500, limit)
	result := metricPageResult(0, 50, 120)
	assert.True(t, result.GetHasMore())
	assert.Equal(t, uint32(120), result.GetTotal())
}

func TestMetricPBConverters(t *testing.T) {
	now := time.Now().UTC()
	series := seriesToPB(monmetrics.MetricSeries{SeriesID: "s1", ServiceName: "svc", MetricName: "cpu", LastSeenAt: now, IsStale: true})
	assert.Equal(t, "s1", series.GetSeriesId())
	assert.True(t, series.GetStale())
	latest := latestToPB(monmetrics.MetricLatest{SeriesID: "s1", Value: 1.5, ObservedAt: now, IntervalSeconds: 30})
	assert.InDelta(t, 1.5, latest.GetValue(), 0.001)
	eval := evaluationToPB(&monmetrics.RuleEvaluation{
		EvaluationID: "e1", SpaceID: "moox_system", RuleID: "r1", EvaluatedAt: now,
		Result: true, Status: domain.AlertStatusFiring,
		Conditions: []monmetrics.ConditionResult{{ConditionID: "c1", SelectedSeriesCount: 2, Value: 1, Threshold: 0.5, HasData: true, Result: true}},
	})
	require.NotNil(t, eval)
	assert.Len(t, eval.GetConditions(), 1)
	state := stateToPB(monmetrics.MetricRuleStateRow{SpaceID: "moox_system", RuleID: "r1", Status: domain.AlertStatusOK, TriggerCount: 1})
	assert.Equal(t, monitorpb.AlertStatus_ALERT_STATUS_OK, state.GetStatus())
}
