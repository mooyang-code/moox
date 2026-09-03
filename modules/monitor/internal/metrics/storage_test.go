package metrics

import (
	"context"
	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeMetadata struct {
	columns   []*storagepb.DatasetColumn
	dataset   *storagepb.Dataset
	node      *storagepb.DataNode
	space     *storagepb.Space
	nodeErr   error
	nodeCalls int
}

func (f *fakeMetadata) GetSpace(context.Context, *storagepb.GetSpaceReq, ...client.Option) (*storagepb.GetSpaceRsp, error) {
	return &storagepb.GetSpaceRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Space: f.space}, nil
}
func (f *fakeMetadata) GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error) {
	return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Dataset: f.dataset}, nil
}
func (f *fakeMetadata) GetDataNode(context.Context, *storagepb.GetDataNodeReq, ...client.Option) (*storagepb.GetDataNodeRsp, error) {
	f.nodeCalls++
	if f.nodeErr != nil {
		return nil, f.nodeErr
	}
	return &storagepb.GetDataNodeRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Node: f.node}, nil
}
func (f *fakeMetadata) ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq, ...client.Option) (*storagepb.ListDatasetColumnsRsp, error) {
	return &storagepb.ListDatasetColumnsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Columns: f.columns}, nil
}

type fakeAccess struct {
	writes  []*storagepb.RowFieldUpsert
	readReq *storagepb.ReadTimeSeriesRowsReq
}

func (f *fakeAccess) UpsertFields(_ context.Context, req *storagepb.PrimaryUpsertFieldsReq, _ ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	f.writes = append(f.writes, req.GetRows()...)
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}
func (f *fakeAccess) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.readReq = req
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}

func metricsStorageConfig() monconfig.MetricsStorageConfig {
	return monconfig.MetricsStorageConfig{SpaceID: "mooxsys", DatasetID: "dataset_mooxsys_service_metrics", Frequency: "30s", WriteBatchSize: 1}
}
func TestStorageAdapterValidatesReadOnlySchemaAndWritesBoundedRows(t *testing.T) {
	f := &fakeMetadata{space: &storagepb.Space{SpaceId: "mooxsys", Status: "active"}, dataset: &storagepb.Dataset{SpaceId: "mooxsys", DatasetId: "dataset_mooxsys_service_metrics", Status: "active", BindingLocked: true, DataNodeId: "storage-node-0", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"30s"}}, node: &storagepb.DataNode{NodeId: "storage-node-0", Status: "active"}}
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
	if a.writes[0].GetKey().GetTimeSeries().GetSubjectId() != "s1" {
		t.Fatalf("subject id=%q", a.writes[0].GetKey().GetTimeSeries().GetSubjectId())
	}
	if len(a.writes[0].GetAttributes()) != 4 {
		t.Fatalf("write attributes=%v, want complete metric identity", a.writes[0].GetAttributes())
	}
}

func TestStorageAdapterQueryHistorySelectorsUseSeriesIdentity(t *testing.T) {
	a := &fakeAccess{}
	adapter := NewStorageAdapter(a, &fakeMetadata{}, metricsStorageConfig())
	_, err := adapter.QueryHistorySelectors(
		context.Background(),
		[]HistorySelector{{SeriesID: "series-1"}},
		time.Time{},
		time.Time{},
		true,
		10,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.readReq.GetSelectors()) != 1 {
		t.Fatalf("selectors=%d, want 1", len(a.readReq.GetSelectors()))
	}
	if a.readReq.GetSpaceId() != "mooxsys" || a.readReq.GetDatasetId() != "dataset_mooxsys_service_metrics" {
		t.Fatalf("request scope=%s/%s", a.readReq.GetSpaceId(), a.readReq.GetDatasetId())
	}
	key := a.readReq.GetSelectors()[0]
	if key.GetSubjectId() != "series-1" {
		t.Fatalf("query key=%+v, want series subject", key)
	}
	if key.SeriesTag != nil {
		t.Fatal("metrics history selector must omit series_tag")
	}
}
func TestStorageAdapterRejectsUnreadyDataNode(t *testing.T) {
	f := &fakeMetadata{space: &storagepb.Space{Status: "active"}, dataset: &storagepb.Dataset{Status: "active", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"30s"}}}
	adapter := NewStorageAdapter(&fakeAccess{}, f, metricsStorageConfig())
	if err := adapter.ValidateSchema(context.Background()); err == nil {
		t.Fatal("expected missing binding error")
	}
}

func TestStorageAdapterResolvesDataNodeWithoutRouteRPC(t *testing.T) {
	f := &fakeMetadata{space: &storagepb.Space{SpaceId: "mooxsys", Status: "active"}, dataset: &storagepb.Dataset{Status: "active", BindingLocked: true, DataNodeId: "storage-node-0", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"30s"}}, node: &storagepb.DataNode{NodeId: "storage-node-0", Status: "active"}}
	for _, c := range []struct {
		name     string
		typ      storagepb.FieldValueType
		originID string
		required bool
	}{{"value", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "monitor_metric_value", true}, {"labels_json", storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON, "monitor_metric_labels", true}, {"producer_node_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "monitor_metric_producer_node_id", false}, {"producer_version", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "monitor_metric_producer_version", false}, {"message_id", storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, "monitor_metric_message_id", true}} {
		f.columns = append(f.columns, &storagepb.DatasetColumn{ColumnName: c.name, ValueType: c.typ, OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: c.originID, Required: c.required, Status: "active"})
	}
	adapter := NewStorageAdapter(&fakeAccess{}, f, metricsStorageConfig())
	require.NoError(t, adapter.ValidateSchema(context.Background()))
	assert.Equal(t, 1, f.nodeCalls)
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

func TestHistorySelectorForSeries(t *testing.T) {
	selector := HistorySelectorForSeries(MetricSeries{
		SeriesID: "s1", ServiceName: "svc", InstanceID: "i", MetricName: "cpu", MetricType: "gauge",
	})
	assert.Equal(t, "s1", selector.SeriesID)
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
	assert.Equal(t, "s1", row.GetKey().GetTimeSeries().GetSubjectId())
}
