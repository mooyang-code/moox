//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/eventmapper"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
)

func TestViewABDualWriteKeepsLiveValueAcrossBackfill(t *testing.T) {
	ctx := context.Background()
	const secret = "view-ab-e2e-secret"
	service, err := viewservice.New(filepath.Join(t.TempDir(), "view"), secret)
	if err != nil {
		t.Fatal(err)
	}
	auth := &pb.AuthInfo{AppId: "e2e", AppKey: datanode.ServiceAuthKey(secret, "e2e")}
	columns := []*pb.ViewColumn{{ColumnName: "close", OriginId: "prices.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	prepare := func(id string, version uint64) {
		t.Helper()
		rsp, err := service.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: id, Schema: &pb.ViewIndexSchema{
			SpaceId: "quant", ViewId: "ab-view", PrimaryDatasetId: "prices", DatasetIds: []string{"prices"},
			ViewVersion: version, Engine: "duckdb", ViewSchemaHash: "schema-" + id, Columns: columns,
		}})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare %s: rsp=%v err=%v", id, rsp, err)
		}
	}
	row := func(at string, value float64) *pb.ViewIndexRowWrite {
		return &pb.ViewIndexRowWrite{Key: &pb.ViewIndexRowKey{RowKey: &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: at}}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}}}
	}
	if rsp, err := service.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "ab-a", Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "ab-view", PrimaryDatasetId: "prices", DatasetIds: []string{"prices"}, ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema-ab-a", Columns: columns}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare A: rsp=%v err=%v", rsp, err)
	}
	if rsp, err := service.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: "ab-a", Batch: &pb.ViewIndexWriteBatch{ViewRevision: 1, ViewSchemaHash: "schema-ab-a", WriteMode: "LIVE_WRITE", RowWrites: []*pb.ViewIndexRowWrite{row("2026-08-12T00:00:00Z", 100), row("2026-08-12T00:01:00Z", 101)}}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write A: rsp=%v err=%v", rsp, err)
	}
	if err := service.AttachActiveView(&pb.View{SpaceId: "quant", ViewId: "ab-view", PrimaryDatasetId: "prices", DatasetIds: []string{"prices"}, Engine: "duckdb", ActiveIndexId: "ab-a", ActiveViewRevision: 1, DesiredViewRevision: 1, ActiveViewSchemaHash: "schema-ab-a", ActiveColumns: columns, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	prepare("ab-b", 2)
	// The live update arrives while B is being populated. Backfill is allowed
	// to read the older value from A, but must not overwrite this newer value.
	if rsp, err := service.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: "ab-b", Batch: &pb.ViewIndexWriteBatch{ViewRevision: 2, ViewSchemaHash: "schema-ab-b", WriteMode: "LIVE_WRITE", RowWrites: []*pb.ViewIndexRowWrite{row("2026-08-12T00:00:00Z", 200)}}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write B live: rsp=%v err=%v", rsp, err)
	}
	// A normal DatasetRowsUpserted is routed through the View service while the
	// replacement is pending. It must update both A and B, not just the new
	// index, so the active view remains authoritative until the switch.
	liveLocal := &pb.RowsUpserted{SpaceId: "quant", DatasetId: "prices", Rows: []*pb.RowFieldUpsert{
		{Key: &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-12T00:02:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 202}}}}},
	}}
	livePayload, err := eventmapper.ToEventRows(liveLocal)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.HandleDatasetRows(ctx, &eventpb.EventMessage{SpaceId: "quant", SubjectId: "prices"}, livePayload); err != nil {
		t.Fatalf("dual-write DatasetRowsUpserted: %v", err)
	}
	activeRsp, err := service.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "quant", ViewId: "ab-view", TimeRange: &pb.TimeRange{StartTime: "2026-08-12T00:00:00Z", EndTime: "2026-08-12T00:03:00Z"}, Page: &pb.Page{Page: 1, Size: 10}, TotalMode: pb.TotalMode_NONE})
	if err != nil || activeRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(activeRsp.GetRows()) != 3 {
		t.Fatalf("active A did not remain readable during replacement: rsp=%v err=%v", activeRsp, err)
	}
	if err := service.BackfillView(ctx, "quant", "ab-view", 100); err != nil {
		t.Fatal(err)
	}
	if err := service.SwitchView(ctx, "quant", "ab-view", 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	rsp, err := service.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "quant", ViewId: "ab-view", TimeRange: &pb.TimeRange{StartTime: "2026-08-12T00:00:00Z", EndTime: "2026-08-12T00:03:00Z"}, Page: &pb.Page{Page: 1, Size: 10}, TotalMode: pb.TotalMode_NONE})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 3 {
		t.Fatalf("query B: rsp=%v err=%v", rsp, err)
	}
	if got := findClose(rsp.GetRows(), "2026-08-12T00:00:00Z"); got != 200 {
		t.Fatalf("live value was overwritten by backfill: got %v", got)
	}
	if got := findClose(rsp.GetRows(), "2026-08-12T00:01:00Z"); got != 101 {
		t.Fatalf("historical value was not copied to B: got %v", got)
	}
	if got := findClose(rsp.GetRows(), "2026-08-12T00:02:00Z"); got != 202 {
		t.Fatalf("dual-written live value was not retained on B: got %v", got)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := service.ListViewIndexes(ctx, &pb.ListViewIndexesReq{AuthInfo: auth, SpaceId: "quant", ViewId: "ab-view"}); err != nil {
		t.Fatal(err)
	}
}

func findClose(rows []*pb.TimeSeriesRow, at string) float64 {
	for _, row := range rows {
		if row.GetKey().GetDataTime() != at {
			continue
		}
		for _, field := range row.GetFields() {
			if field.GetFieldId() == "close" {
				return field.GetValue().GetDoubleValue()
			}
		}
	}
	return 0
}
