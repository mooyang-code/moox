package metrics

import (
	"context"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMetricMessageStoreNilGuards(t *testing.T) {
	var store *MetricMessageStore
	_, err := store.IsDuplicate(context.Background(), "msg")
	require.Error(t, err)
	_, err = store.CommitIngest(context.Background(), &eventpb.EventMessage{EventId: "m"}, &metricspb.MetricReport{}, nil)
	require.Error(t, err)

	empty := NewMetricMessageStore(nil)
	require.NotNil(t, empty)
	_, err = empty.IsDuplicate(context.Background(), "")
	require.Error(t, err)
	_, err = empty.IsDuplicate(context.Background(), "id")
	require.Error(t, err)
	_, err = empty.CommitIngest(context.Background(), nil, nil, nil)
	require.Error(t, err)
	_, err = empty.CommitIngest(context.Background(), &eventpb.EventMessage{}, &metricspb.MetricReport{}, nil)
	require.Error(t, err)
	assert.Equal(t, empty.DedupeRetention.Hours(), float64(7*24))
}

func TestMonotonicMetricRecognizesCanonicalModuleNames(t *testing.T) {
	for _, name := range []string{
		"moox_factor_last_success_timestamp_seconds",
		"moox_monitor_business_watermark_timestamp_seconds",
		"moox_strategy_input_watermark_timestamp_seconds",
		"moox_trade_metrics_errors_total",
		"moox_archive_metrics_last_error_timestamp_seconds",
	} {
		require.True(t, monotonicMetric(name), name)
	}
	require.False(t, monotonicMetric("moox_factor_runs_total"))
}

func metricMessageStoreForTest(t *testing.T, db *store.Store) *MetricMessageStore {
	t.Helper()
	result, err := store.WithDatabase(db, NewMetricMessageStore)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func metricRuleStoreForTest(t *testing.T, db *store.Store) *MetricRuleStore {
	t.Helper()
	result, err := store.WithDatabase(db, NewMetricRuleStore)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
