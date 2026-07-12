package metrics

import (
	"context"
	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeMetadata struct {
	columns []*storagepb.DatasetColumn
	routes  []*storagepb.PrimaryStoreRoute
	dataset *storagepb.Dataset
	space   *storagepb.Space
}

func (f *fakeMetadata) GetSpace(context.Context, *storagepb.GetSpaceReq, ...client.Option) (*storagepb.GetSpaceRsp, error) {
	return &storagepb.GetSpaceRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Space: f.space}, nil
}
func (f *fakeMetadata) GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error) {
	return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Dataset: f.dataset}, nil
}
func (f *fakeMetadata) ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq, ...client.Option) (*storagepb.ListDatasetColumnsRsp, error) {
	return &storagepb.ListDatasetColumnsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Columns: f.columns}, nil
}
func (f *fakeMetadata) ListPrimaryStoreRoutes(context.Context, *storagepb.ListPrimaryStoreRoutesReq, ...client.Option) (*storagepb.ListPrimaryStoreRoutesRsp, error) {
	return &storagepb.ListPrimaryStoreRoutesRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, PrimaryStoreRoutes: f.routes}, nil
}

type fakeAccess struct {
	writes  []*storagepb.TimeSeriesRow
	readReq *storagepb.ReadTimeSeriesRowsReq
}

func (f *fakeAccess) WriteTimeSeriesRows(_ context.Context, req *storagepb.WriteTimeSeriesRowsReq, _ ...client.Option) (*storagepb.WriteTimeSeriesRowsRsp, error) {
	f.writes = append(f.writes, req.GetRows()...)
	return &storagepb.WriteTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}
func (f *fakeAccess) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.readReq = req
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}

func metricsStorageConfig() monconfig.MetricsStorageConfig {
	return monconfig.MetricsStorageConfig{SpaceID: "moox_system", DatasetID: "moox_service_metrics", Frequency: "30s", WriteBatchSize: 1}
}
func TestStorageAdapterValidatesReadOnlySchemaAndWritesBoundedRows(t *testing.T) {
	f := &fakeMetadata{space: &storagepb.Space{SpaceId: "moox_system", Status: "active"}, dataset: &storagepb.Dataset{SpaceId: "moox_system", DatasetId: "moox_service_metrics", Status: "active", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"30s"}}, routes: []*storagepb.PrimaryStoreRoute{{SpaceId: "moox_system", DatasetId: "moox_service_metrics", SubjectPattern: "*", HashRule: "subject_id", Status: "active"}}}
	for _, c := range []struct {
		name     string
		typ      storagepb.FieldValueType
		originID string
		required bool
	}{{"value", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "monitor_metric_value", true}, {"labels_json", storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON, "monitor_metric_labels", true}, {"producer_node_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "monitor_metric_producer_node_id", false}, {"producer_version", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "monitor_metric_producer_version", false}, {"message_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "monitor_metric_message_id", true}} {
		f.columns = append(f.columns, &storagepb.DatasetColumn{ColumnName: c.name, ValueType: c.typ, OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: c.originID, Required: c.required, Status: "active"})
	}
	a := &fakeAccess{}
	adapter := NewStorageAdapter(a, f, metricsStorageConfig())
	if err := adapter.ValidateSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	samples := []Sample{{SeriesID: "s1", ServiceName: "svc", InstanceID: "i", MetricName: "m", MetricType: "gauge", ObservedAt: time.Unix(1, 0).UTC(), Value: 2, LabelsJSON: "{}", MessageID: "msg"}, {SeriesID: "s2", ServiceName: "svc", InstanceID: "i", MetricName: "m", MetricType: "gauge", ObservedAt: time.Unix(2, 0).UTC(), Value: 3, LabelsJSON: "{}", MessageID: "msg2"}}
	if err := adapter.WriteSamples(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	if len(a.writes) != 2 {
		t.Fatalf("writes=%d, want 2", len(a.writes))
	}
	if a.writes[0].GetKey().GetSubjectId() != "s1" {
		t.Fatalf("subject id=%q", a.writes[0].GetKey().GetSubjectId())
	}
	if len(a.writes[0].GetKey().GetDimensions()) != 4 {
		t.Fatalf("write dimensions=%v, want complete metric identity", a.writes[0].GetKey().GetDimensions())
	}
}

func TestStorageAdapterQueryHistorySelectorsIncludeDimensions(t *testing.T) {
	a := &fakeAccess{}
	adapter := NewStorageAdapter(a, &fakeMetadata{}, metricsStorageConfig())
	_, err := adapter.QueryHistorySelectors(context.Background(), []HistorySelector{{SeriesID: "series-1", Dimensions: map[string]string{
		"service_name": "svc", "instance_id": "instance", "metric_name": "requests_total", "metric_type": "counter",
	}}}, time.Time{}, time.Time{}, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.readReq.GetKeys()) != 1 {
		t.Fatalf("keys=%d, want 1", len(a.readReq.GetKeys()))
	}
	key := a.readReq.GetKeys()[0]
	if key.GetSubjectId() != "series-1" || key.GetDimensions()["metric_name"] != "requests_total" {
		t.Fatalf("query key=%+v, want subject and dimensions", key)
	}
}
func TestStorageAdapterRejectsMissingRoute(t *testing.T) {
	f := &fakeMetadata{space: &storagepb.Space{Status: "active"}, dataset: &storagepb.Dataset{Status: "active", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"30s"}}}
	adapter := NewStorageAdapter(&fakeAccess{}, f, metricsStorageConfig())
	if err := adapter.ValidateSchema(context.Background()); err == nil {
		t.Fatal("expected missing columns/route error")
	}
}

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
