//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"testing"

	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	viewduckdb "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestViewQueryFilterSortPageAndTotalE2E(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "view")
	columns := []*viewindex.Column{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT},
	}
	schema := viewindex.ViewIndexSchema{SpaceID: "quant", DatasetID: "prices", DatasetCreatedAt: followEnvDatasetCreatedAt, ViewVersion: 1, Engine: "duckdb", SchemaHash: "schema-1", Columns: columns}
	prebuildViewIndex(t, ctx, root, schema, []viewindex.RowWrite{
		viewRow("BTC", "2026-07-20T00:00:00Z", 100, 10),
		viewRow("ETH", "2026-07-20T00:01:00Z", 200, 20),
		viewRow("SOL", "2026-07-20T00:02:00Z", 50, 5),
	})
	service, err := viewservice.New(root)
	if err != nil {
		t.Fatal(err)
	}

	attachPrebuiltView(t, service, "quant", "prices", columns)

	rsp, err := service.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		SpaceId: "quant", DatasetId: "prices",
		TimeRange: &pb.TimeRange{StartTime: "2026-07-20T00:00:00Z", EndTime: "2026-07-20T00:03:00Z"},
		Filter:    &pb.FilterSpec{Groups: []*pb.FilterGroup{{Conds: []*pb.FilterCond{{Column: "close", Op: pb.FilterOp_FILTER_OP_GTE, Values: []*pb.TypedValue{{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}}}}}},
		Sorts:     []*pb.SortSpec{{FieldName: "close", Desc: true}}, ColumnNames: []string{"close"},
		Page: &pb.Page{Page: 1, Size: 1}, TotalMode: pb.TotalMode_FORCE_EXACT,
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 {
		t.Fatalf("query: rsp=%v err=%v", rsp, err)
	}
	if rsp.GetRows()[0].GetKey().GetTimeSeries().GetSubjectId() != "ETH" || rsp.GetRows()[0].GetFields()[0].GetValue().GetDoubleValue() != 200 {
		t.Fatalf("unexpected first page: %v", rsp.GetRows()[0])
	}
	if rsp.GetPageResult().GetTotal() != 2 || !rsp.GetPageResult().GetHasMore() {
		t.Fatalf("unexpected page result: %v", rsp.GetPageResult())
	}
}

func viewRow(subject, dataTime string, close float64, volume int64) viewindex.RowWrite {
	return viewindex.RowWrite{
		Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Frequency: "1m", DataTime: dataTime}}}},
		Fields: []*pb.FieldValue{
			{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: close}}},
			{FieldId: "volume", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: volume}}},
		},
	}
}

// prebuildViewIndex 在 View 服务打开索引目录之前直接用 DuckDB 引擎建好索引并写入 rows（View 进程不对外提供索引写入接口）。
func prebuildViewIndex(t *testing.T, ctx context.Context, root string, schema viewindex.ViewIndexSchema, rows []viewindex.RowWrite) {
	t.Helper()
	manager, err := viewduckdb.OpenIndexManager(viewduckdb.IndexManagerOptions{Root: filepath.Join(root, "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	indexID := viewindex.IndexID(schema.SpaceID, schema.DatasetID)
	if err := manager.Prepare(ctx, indexID, schema); err != nil {
		t.Fatal(err)
	}
	// 记下跟随进度才算建好；游标不属于真实的存储，跟随器启动后会按断档重建。
	if err := manager.SaveFollowState(ctx, indexID, viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: "prebuilt", Sequence: 1}, BuiltSequence: 1}); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return
	}
	if err := manager.Write(ctx, indexID, viewindex.ViewIndexWriteBatch{RowWrites: rows, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite}); err != nil {
		t.Fatal(err)
	}
}

// attachPrebuiltView 让 View 服务按数据集元数据挂接预先建好的索引（与启动恢复相同）。
func attachPrebuiltView(t *testing.T, service *viewservice.Service, spaceID, datasetID string, columns []*viewindex.Column) {
	t.Helper()
	metadata := &followEnvMetadata{}
	items := make([]*pb.DatasetColumn, 0, len(columns))
	for _, column := range columns {
		items = append(items, &pb.DatasetColumn{SpaceId: spaceID, DatasetId: datasetID, ColumnName: column.ColumnName, DisplayName: column.ColumnName, ValueType: column.ValueType, Status: "active"})
	}
	metadata.addDataset(&pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Frequency: "1m", Status: "active", CreatedAt: followEnvDatasetCreatedAt}, items...)
	if err := service.RestoreActiveViews(context.Background(), viewservice.MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
}
