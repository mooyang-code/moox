//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"testing"

	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	viewduckdb "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	"github.com/mooyang-code/moox/modules/storage/internal/serviceauth"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestViewQueryFilterSortPageAndTotalE2E(t *testing.T) {
	ctx := context.Background()
	const secret = "view-e2e-secret"
	root := filepath.Join(t.TempDir(), "view")
	columns := []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT},
	}
	schema := viewindex.ViewIndexSchema{SpaceID: "quant", ViewID: "prices-view", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "schema-1", Columns: columns}
	prebuildViewIndex(t, ctx, root, schema, []viewindex.RowWrite{
		viewRow("BTC", "2026-07-20T00:00:00Z", 100, 10),
		viewRow("ETH", "2026-07-20T00:01:00Z", 200, 20),
		viewRow("SOL", "2026-07-20T00:02:00Z", 50, 5),
	})
	service, err := viewservice.New(root, secret)
	if err != nil {
		t.Fatal(err)
	}

	auth := &pb.AuthInfo{AppId: "e2e", AppKey: serviceauth.Key(secret, "e2e")}
	if err := service.AttachActiveView(context.Background(), &pb.View{SpaceId: "quant", ViewId: "prices-view", DatasetId: "prices", Engine: "duckdb", Status: "active"},
		viewindex.ViewIndexStats{Exists: true, DatasetID: "prices", ViewVersion: 1, SchemaHash: "schema-1", Columns: columns}); err != nil {
		t.Fatal(err)
	}

	rsp, err := service.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "quant", ViewId: "prices-view",
		TimeRange: &pb.TimeRange{StartTime: "2026-07-20T00:00:00Z", EndTime: "2026-07-20T00:03:00Z"},
		Filter:    &pb.FilterSpec{Groups: []*pb.FilterGroup{{Conds: []*pb.FilterCond{{Column: "close", Op: pb.FilterOp_FILTER_OP_GTE, Values: []*pb.TypedValue{{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}}}}}},
		Sorts:     []*pb.SortSpec{{FieldName: "close", Desc: true}}, ColumnNames: []string{"close"},
		Page: &pb.Page{Page: 1, Size: 1}, TotalMode: pb.TotalMode_FORCE_EXACT,
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 {
		t.Fatalf("query: rsp=%v err=%v", rsp, err)
	}
	if rsp.GetRows()[0].GetKey().GetSubjectId() != "ETH" || rsp.GetRows()[0].GetFields()[0].GetValue().GetDoubleValue() != 200 {
		t.Fatalf("unexpected first page: %v", rsp.GetRows()[0])
	}
	if rsp.GetPageResult().GetTotal() != 2 || !rsp.GetPageResult().GetHasMore() {
		t.Fatalf("unexpected page result: %v", rsp.GetPageResult())
	}
}

func viewRow(subject, dataTime string, close float64, volume int64) viewindex.RowWrite {
	return viewindex.RowWrite{
		Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Freq: "1m", DataTime: dataTime}}}},
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
	indexID := viewindex.ViewIndexID(schema.SpaceID, schema.ViewID)
	if err := manager.Prepare(ctx, indexID, schema); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return
	}
	if err := manager.Write(ctx, indexID, viewindex.ViewIndexWriteBatch{RowWrites: rows, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite}); err != nil {
		t.Fatal(err)
	}
}
