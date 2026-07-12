package metrics

import (
	"testing"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/stretchr/testify/assert"
)

func TestReduceTimeSeriesCoversReducersAndCounterReset(t *testing.T) {
	base := time.Unix(100, 0).UTC()
	values := []TimedValue{
		{At: base.Add(20 * time.Second), Value: 9},
		{At: base, Value: 10},
		{At: base.Add(10 * time.Second), Value: 4},
	}

	got, ok := ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_CURRENT, values)
	assert.True(t, ok)
	assert.Equal(t, 9.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_AVG, values)
	assert.True(t, ok)
	assert.Equal(t, 23.0/3.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_MIN, values)
	assert.True(t, ok)
	assert.Equal(t, 4.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_MAX, values)
	assert.True(t, ok)
	assert.Equal(t, 10.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_SUM, values)
	assert.True(t, ok)
	assert.Equal(t, 23.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_INCREASE, values)
	assert.True(t, ok)
	assert.Equal(t, 9.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_RATE, values)
	assert.True(t, ok)
	assert.Equal(t, 9.0/20.0, got)

	_, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_RATE, values[:1])
	assert.False(t, ok)
	_, ok = ReduceTimeSeries(monitorpb.TimeReducer(99), values)
	assert.False(t, ok)
	_, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_CURRENT, nil)
	assert.False(t, ok)
}

func TestReduceSeriesLabelsAndNoDataHelpers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reducer monitorpb.SeriesReducer
		want    float64
		ok      bool
	}{
		{"avg", monitorpb.SeriesReducer_SERIES_REDUCER_AVG, 4, true},
		{"min", monitorpb.SeriesReducer_SERIES_REDUCER_MIN, 2, true},
		{"max", monitorpb.SeriesReducer_SERIES_REDUCER_MAX, 6, true},
		{"sum", monitorpb.SeriesReducer_SERIES_REDUCER_SUM, 12, true},
		{"unknown", monitorpb.SeriesReducer(99), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ReduceSeries(tc.reducer, []float64{2, 4, 6})
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
	_, ok := ReduceSeries(monitorpb.SeriesReducer_SERIES_REDUCER_AVG, nil)
	assert.False(t, ok)

	assert.True(t, labelsMatch(`{"env":"prod","zone":"gz"}`, []*monitorpb.LabelMatcher{{Name: "env", Value: "prod"}}))
	assert.True(t, labelsMatch(`{"env":"prod"}`, []*monitorpb.LabelMatcher{{Name: "zone", Value: "gz", Negate: true}}))
	assert.False(t, labelsMatch(`{"env":"prod"}`, []*monitorpb.LabelMatcher{{Name: "env", Value: "prod", Negate: true}}))
	assert.False(t, labelsMatch(`not-json`, []*monitorpb.LabelMatcher{{Name: "env", Value: "prod"}}))
	assert.False(t, labelsMatch(`{"env":"prod"}`, []*monitorpb.LabelMatcher{{Name: "env", Value: "dev"}}))

	assert.Equal(t, "keep_state", noDataReason(monitorpb.NoDataPolicy_NO_DATA_POLICY_KEEP_STATE))
	assert.Equal(t, "ok", noDataReason(monitorpb.NoDataPolicy_NO_DATA_POLICY_OK))
	assert.Equal(t, "firing", noDataReason(monitorpb.NoDataPolicy_NO_DATA_POLICY_FIRING))
	assert.Equal(t, "unspecified", noDataReason(monitorpb.NoDataPolicy(99)))
	assert.False(t, Compare(monitorpb.CompareOperator(99), 1, 1))
}
