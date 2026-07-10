package metrics

import (
	"context"
	"testing"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
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

type fakeAccess struct{ writes []*storagepb.TimeSeriesRow }

func (f *fakeAccess) WriteTimeSeriesRows(_ context.Context, req *storagepb.WriteTimeSeriesRowsReq, _ ...client.Option) (*storagepb.WriteTimeSeriesRowsRsp, error) {
	f.writes = append(f.writes, req.GetRows()...)
	return &storagepb.WriteTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}
func (f *fakeAccess) ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
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
}
func TestStorageAdapterRejectsMissingRoute(t *testing.T) {
	f := &fakeMetadata{space: &storagepb.Space{Status: "active"}, dataset: &storagepb.Dataset{Status: "active", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"30s"}}}
	adapter := NewStorageAdapter(&fakeAccess{}, f, metricsStorageConfig())
	if err := adapter.ValidateSchema(context.Background()); err == nil {
		t.Fatal("expected missing columns/route error")
	}
}
