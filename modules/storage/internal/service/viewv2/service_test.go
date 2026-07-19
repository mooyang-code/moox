package viewv2

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestViewIndexAndDataViewExplicitKeyFlow(t *testing.T) {
	svc := New(filepath.Join(t.TempDir(), "views"))
	ctx := context.Background()
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{IndexId: "prices-view", Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema-1"}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare: rsp=%v err=%v", rsp, err)
	}
	key := &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}}
	value := &pb.FieldValue{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}
	if rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{IndexId: "prices-view", Batch: &pb.ViewIndexApplyBatch{ViewRevision: 1, ViewSchemaHash: "schema-1", WriteMode: "LIVE_WRITE", RowWrites: []*pb.ViewIndexRowWrite{{Key: &pb.ViewIndexRowKey{RowKey: key}, Fields: []*pb.FieldValue{value}}}}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("apply: rsp=%v err=%v", rsp, err)
	}
	rsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{ViewId: "prices-view", Keys: []*pb.TimeSeriesKey{{SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}, ColumnNames: []string{"close"}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 || len(rsp.GetRows()[0].GetFields()) != 1 {
		t.Fatalf("query: rsp=%v err=%v", rsp, err)
	}
}
