package e2e

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	primarystorev2 "github.com/mooyang-code/moox/modules/storage/internal/service/primarystorev2"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewv2"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// TestPrimaryDataNodeViewFlow exercises the process boundaries in one test:
// PrimaryStore routes two Datasets to independent DataNodes, then ViewIndex
// materializes and serves one of the resulting RowKeys.
func TestPrimaryDataNodeViewFlow(t *testing.T) {
	ctx := context.Background()
	const secret = "e2e-secret"
	newNode := func(id string) *datanode.Service {
		node, err := datanode.NewService(datanode.Options{NodeID: id, AuthSecret: secret, Pebble: pebble.Options{NodeID: id, Path: filepath.Join(t.TempDir(), id)}})
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	nodeA, nodeB := newNode("node-a"), newNode("node-b")
	defer nodeA.Close()
	defer nodeB.Close()
	nodes := map[string]pb.DataNodeService{"prices": nodeA, "factors": nodeB}
	primary, err := primarystorev2.New(primarystorev2.Options{
		Resolver: func(_ context.Context, _, dataset string) (pb.DataNodeService, error) { return nodes[dataset], nil },
		AuthSigner: func(in *pb.AuthInfo) (*pb.AuthInfo, error) {
			clone := *in
			clone.AppKey = datanode.ServiceAuthKey(secret, clone.AppId)
			return &clone, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := &pb.AuthInfo{AppId: "storage-primary"}
	priceKey := &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}}
	factorKey := &pb.RowKey{SpaceId: "quant", DatasetId: "factors", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}}
	priceField := &pb.FieldValue{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}
	factorField := &pb.FieldValue{FieldId: "momentum", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.2}}}
	if rsp, err := primary.WriteFields(ctx, &pb.PrimaryWriteFieldsReq{AuthInfo: auth, Rows: []*pb.RowFieldUpsert{{Key: priceKey, Fields: []*pb.FieldValue{priceField}}, {Key: factorKey, Fields: []*pb.FieldValue{factorField}}}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetKeys()) != 2 {
		t.Fatalf("primary write: rsp=%v err=%v", rsp, err)
	}
	read, err := primary.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: auth, Keys: []*pb.RowKey{priceKey, factorKey}, FieldIds: []string{"close", "momentum"}})
	if err != nil || read.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(read.GetRows()) != 2 {
		t.Fatalf("primary read: rsp=%v err=%v", read, err)
	}

	view := viewv2.New(filepath.Join(t.TempDir(), "view"))
	if rsp, err := view.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{IndexId: "prices-view", Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema-1"}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("view prepare: rsp=%v err=%v", rsp, err)
	}
	if rsp, err := view.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{IndexId: "prices-view", Batch: &pb.ViewIndexApplyBatch{ViewRevision: 1, ViewSchemaHash: "schema-1", WriteMode: "LIVE_WRITE", RowWrites: []*pb.ViewIndexRowWrite{{Key: &pb.ViewIndexRowKey{RowKey: priceKey}, Fields: []*pb.FieldValue{priceField}}}}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("view apply: rsp=%v err=%v", rsp, err)
	}
	result, err := view.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{ViewId: "prices-view", Keys: []*pb.TimeSeriesKey{{SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}, ColumnNames: []string{"close"}})
	if err != nil || result.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(result.GetRows()) != 1 {
		t.Fatalf("view query: rsp=%v err=%v", result, err)
	}
}
