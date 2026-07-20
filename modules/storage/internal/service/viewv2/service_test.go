package viewv2

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestViewIndexAndDataViewExplicitKeyFlow(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "prices-view", Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "schema-1"}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare: rsp=%v err=%v", rsp, err)
	}
	key := &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}}
	value := &pb.FieldValue{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}
	if rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: "prices-view", Batch: &pb.ViewIndexApplyBatch{ViewRevision: 1, ViewSchemaHash: "schema-1", WriteMode: "LIVE_WRITE", RowWrites: []*pb.ViewIndexRowWrite{{Key: &pb.ViewIndexRowKey{RowKey: key}, Fields: []*pb.FieldValue{value}}}}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("apply: rsp=%v err=%v", rsp, err)
	}
	rsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, ViewId: "prices-view", Keys: []*pb.TimeSeriesKey{{SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}, ColumnNames: []string{"close"}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 || len(rsp.GetRows()[0].GetFields()) != 1 {
		t.Fatalf("query: rsp=%v err=%v", rsp, err)
	}
}

func TestViewServiceRequiresSecretAndAuth(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "views"), ""); err == nil {
		t.Fatal("expected missing view auth secret to be rejected")
	}
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
		IndexId: "idx",
		Schema:  &pb.ViewIndexSchema{SpaceId: "s", ViewId: "v", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestViewDatasetMappingIncludesSpace(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	for _, space := range []string{"space-a", "space-b"} {
		rsp, err := svc.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
			AuthInfo: auth,
			IndexId:  space + "-view",
			Schema: &pb.ViewIndexSchema{
				SpaceId:        space,
				ViewId:         "view",
				ViewVersion:    1,
				Engine:         "bleve",
				ViewSchemaHash: "hash",
				Columns:        []*pb.ViewColumn{{OriginId: "shared.value", ColumnName: "value"}},
			},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("space=%s rsp=%v err=%v", space, rsp, err)
		}
	}
	if len(svc.byData[datasetRef{spaceID: "space-a", datasetID: "shared"}]) != 1 ||
		len(svc.byData[datasetRef{spaceID: "space-b", datasetID: "shared"}]) != 1 {
		t.Fatalf("byData=%v", svc.byData)
	}
}
