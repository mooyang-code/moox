package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoundedPage(t *testing.T) {
	offset, limit := boundedPage(-1, 0)
	assert.Equal(t, 0, offset)
	assert.Equal(t, 50, limit)
	offset, limit = boundedPage(10, 1000)
	assert.Equal(t, 10, offset)
	assert.Equal(t, 500, limit)
}

func TestListServicesForFiltersBeforeApplyingLimit(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	messageStore := metricMessageStoreForTest(t, mgr)
	require.NoError(t, messageStore.db.Create([]MetricService{
		{ServiceName: "unrelated", InstanceID: "unrelated@node-a", NodeID: "node-a", BootID: "b1"},
		{ServiceName: "selected", InstanceID: "selected@node-a", NodeID: "node-a", BootID: "b2"},
		{ServiceName: "selected", InstanceID: "selected-2@node-a", NodeID: "node-a", BootID: "b3"},
	}).Error)
	rows, err := NewCatalog(messageStore).ListServicesFor(context.Background(), []string{"selected"}, "node-a", 2)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "selected", rows[0].ServiceName)
	_, err = NewCatalog(messageStore).ListServicesFor(context.Background(), []string{"selected"}, "node-a", 1)
	require.Error(t, err)
}

func TestCanonicalJSON(t *testing.T) {
	assert.Equal(t, `{"a":1}`, canonicalJSON(`{"a":1}`))
	assert.Equal(t, "not-json", canonicalJSON("not-json"))
}

func TestMetricCatalogNoDataAfter(t *testing.T) {
	c := NewCatalog(nil)
	assert.Equal(t, 2*time.Minute, c.NoDataAfter())
	c.SetNoDataAfter(5 * time.Minute)
	assert.Equal(t, 5*time.Minute, c.NoDataAfter())
}

func TestMetricCatalogTimeScansRFC3339(t *testing.T) {
	var got metricCatalogTime
	require.NoError(t, got.Scan("2026-07-15T04:17:48.123456789Z"))
	assert.Equal(t, "2026-07-15T04:17:48.123456789Z", got.UTC().Format(time.RFC3339Nano))
}
