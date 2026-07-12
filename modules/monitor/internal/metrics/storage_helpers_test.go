package metrics

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeTarget(t *testing.T) {
	assert.Equal(t, "ip://127.0.0.1:20102", normalizeTarget("", "20102"))
	assert.Equal(t, "ip://127.0.0.1:20102", normalizeTarget("127.0.0.1:20102", "20102"))
	assert.Equal(t, "http://storage:20102", normalizeTarget("http://storage:20102", "20102"))
}

func TestStorageHelpers(t *testing.T) {
	assert.True(t, isActive(" active "))
	assert.False(t, isActive("deleted"))
	assert.True(t, contains([]string{"30s", "1m"}, "30s"))
	assert.Equal(t, []string{"a", "b"}, sortedKeys(map[string]columnContract{"b": {}, "a": {}}))
	require.NoError(t, storageOK("action", &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}))
	require.Error(t, storageOK("action", &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "fail"}))
}

func TestCloneStringMapAndHistorySelector(t *testing.T) {
	assert.Nil(t, cloneStringMap(nil))
	cloned := cloneStringMap(map[string]string{"a": "1"})
	cloned["a"] = "2"
	assert.Equal(t, "1", cloneStringMap(map[string]string{"a": "1"})["a"])
	selector := HistorySelectorForSeries(MetricSeries{
		SeriesID: "s1", ServiceName: "svc", InstanceID: "i", MetricName: "cpu", MetricType: "gauge",
	})
	assert.Equal(t, "s1", selector.SeriesID)
	assert.Equal(t, "svc", selector.Dimensions["service_name"])
}

func TestSchemaStatusNilAdapter(t *testing.T) {
	var adapter *StorageAdapter
	status := adapter.SchemaStatus()
	assert.Contains(t, status.Error, "nil")
}

func TestNewStorageAdapterInitialState(t *testing.T) {
	adapter := NewStorageAdapter(nil, nil, metricsStorageConfig())
	status := adapter.SchemaStatus()
	assert.False(t, status.Valid)
	assert.Contains(t, status.Error, "not been checked")
}

func TestSampleRowUsesConfig(t *testing.T) {
	cfg := metricsStorageConfig()
	sample := Sample{
		SeriesID: "s1", ServiceName: "svc", InstanceID: "i", MetricName: "cpu",
		MetricType: "gauge", ObservedAt: time.Unix(10, 0).UTC(), Value: 1.5,
		LabelsJSON: `{}`, MessageID: "m1",
	}
	row := sampleRow(cfg, sample)
	assert.Equal(t, cfg.SpaceID, row.GetKey().GetSpaceId())
	assert.Equal(t, "s1", row.GetKey().GetSubjectId())
}
