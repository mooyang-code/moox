//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestViewQueryFilterSortPageAndTotalE2E(t *testing.T) {
	ctx := context.Background()
	const secret = "view-e2e-secret"
	service, err := viewservice.New(filepath.Join(t.TempDir(), "view"), secret)
	if err != nil {
		t.Fatal(err)
	}

	auth := &pb.AuthInfo{AppId: "e2e", AppKey: datanode.ServiceAuthKey(secret, "e2e")}
	if rsp, err := service.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
		AuthInfo: auth,
		IndexId:  "prices-view",
		Schema: &pb.ViewIndexSchema{
			SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices", ViewVersion: 1,
			Engine: "duckdb", ViewSchemaHash: "schema-1",
			Columns: []*pb.ViewColumn{
				{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
				{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT},
			},
		},
	}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare: rsp=%v err=%v", rsp, err)
	}
	rows := []*pb.ViewIndexRowWrite{
		viewRow("BTC", "2026-07-20T00:00:00Z", 100, 10),
		viewRow("ETH", "2026-07-20T00:01:00Z", 200, 20),
		viewRow("SOL", "2026-07-20T00:02:00Z", 50, 5),
	}
	if rsp, err := service.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: "prices-view", Batch: &pb.ViewIndexWriteBatch{ViewRevision: 1, ViewSchemaHash: "schema-1", WriteMode: "LIVE_WRITE", RowWrites: rows}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write: rsp=%v err=%v", rsp, err)
	}
	if err := service.AttachActiveView(&pb.View{SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices", Engine: "duckdb", ActiveIndexId: "prices-view", ActiveViewRevision: 1, ActiveViewSchemaHash: "schema-1", ActiveColumns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT}}, Status: "active"}); err != nil {
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

func viewRow(subject, dataTime string, close float64, volume int64) *pb.ViewIndexRowWrite {
	return &pb.ViewIndexRowWrite{
		Key: &pb.ViewIndexRowKey{RowKey: &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Freq: "1m", DataTime: dataTime}}}},
		Fields: []*pb.FieldValue{
			{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: close}}},
			{FieldId: "volume", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: volume}}},
		},
	}
}
