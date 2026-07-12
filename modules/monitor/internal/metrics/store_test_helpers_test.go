package metrics

import (
	"testing"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

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
