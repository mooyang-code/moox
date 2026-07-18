package viewindex

import (
	"context"
	"errors"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"testing"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestClientImplementsLifecycleAndTypedQueries(t *testing.T) {
	proxy := &fakeViewIndexProxy{}
	remote := newClientWithProxy("duckdb", proxy)
	indexID := ViewIndexID("crypto", "spot", SlotA)
	if err := remote.Prepare(context.Background(), indexID, ViewIndexSchema{
		SpaceID: "crypto", ViewID: "spot", Engine: "duckdb", SchemaHash: "schema-1",
	}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if proxy.prepared.GetEngine() != "duckdb" || proxy.prepared.GetIndexId() != indexID {
		t.Fatalf("prepared request = %+v", proxy.prepared)
	}
	stats, err := remote.Stat(context.Background(), indexID)
	if err != nil || stats.EntryCount != 7 || stats.PhysicalBytes != 1024 {
		t.Fatalf("Stat = %+v err=%v", stats, err)
	}
	_, rows, page, err := remote.QueryTimeSeriesRows(context.Background(), indexID, &pb.QueryTimeSeriesRowsReq{})
	if err != nil || len(rows) != 1 || page.GetSize() != 25 {
		t.Fatalf("QueryTimeSeriesRows rows=%d page=%+v err=%v", len(rows), page, err)
	}
}

func TestClientReturnsRetInfoErrors(t *testing.T) {
	proxy := &fakeViewIndexProxy{fail: true}
	remote := newClientWithProxy("duckdb", proxy)
	if _, err := remote.Stat(context.Background(), "bad"); err == nil {
		t.Fatal("Stat error = nil")
	}
}

func TestLocalClientUsesOwnerWriteFence(t *testing.T) {
	duck := &fakeManagedEngine{name: "duckdb"}
	owner := NewService(Options{Engines: map[string]ManagedEngine{"duckdb": duck}})
	local := NewLocalClient(owner, "duckdb")
	indexID := ViewIndexID("crypto", "spot", SlotA)
	if err := local.Prepare(context.Background(), indexID, ViewIndexSchema{
		SpaceID: "crypto", ViewID: "spot", ViewVersion: 2, Engine: "duckdb", SchemaHash: "schema-1",
	}); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := local.Write(context.Background(), indexID, BatchWrite{ViewVersion: 1, SchemaHash: "schema-1"}); err == nil {
		t.Fatal("local client bypassed owner write fence")
	}
}

type fakeViewIndexProxy struct {
	prepared *pb.PrepareViewIndexReq
	fail     bool
}

func (p *fakeViewIndexProxy) ret() *pb.RetInfo {
	if p.fail {
		return &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "owner failed"}
	}
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}
}

func (p *fakeViewIndexProxy) PrepareViewIndex(_ context.Context, req *pb.PrepareViewIndexReq, _ ...client.Option) (*pb.PrepareViewIndexRsp, error) {
	p.prepared = req
	return &pb.PrepareViewIndexRsp{RetInfo: p.ret()}, nil
}

func (p *fakeViewIndexProxy) ApplyViewIndex(context.Context, *pb.ApplyViewIndexReq, ...client.Option) (*pb.ApplyViewIndexRsp, error) {
	return &pb.ApplyViewIndexRsp{RetInfo: p.ret()}, nil
}

func (p *fakeViewIndexProxy) StatViewIndex(context.Context, *pb.StatViewIndexReq, ...client.Option) (*pb.StatViewIndexRsp, error) {
	return &pb.StatViewIndexRsp{RetInfo: p.ret(), Stats: &pb.ViewIndexStats{Exists: true, EntryCount: 7, PhysicalBytes: 1024}}, nil
}

func (p *fakeViewIndexProxy) RemoveViewIndex(context.Context, *pb.RemoveViewIndexReq, ...client.Option) (*pb.RemoveViewIndexRsp, error) {
	return &pb.RemoveViewIndexRsp{RetInfo: p.ret()}, nil
}

func (p *fakeViewIndexProxy) ListViewIndexes(context.Context, *pb.ListViewIndexesReq, ...client.Option) (*pb.ListViewIndexesRsp, error) {
	return &pb.ListViewIndexesRsp{RetInfo: p.ret()}, nil
}

func (p *fakeViewIndexProxy) QueryTimeSeriesIndex(context.Context, *pb.QueryTimeSeriesIndexReq, ...client.Option) (*pb.QueryTimeSeriesIndexRsp, error) {
	return &pb.QueryTimeSeriesIndexRsp{RetInfo: p.ret(), Rows: []*pb.TimeSeriesRow{{}}, PageResult: &pb.PageResult{Page: 1, Size: 25}}, nil
}

func (p *fakeViewIndexProxy) SearchRecordIndex(context.Context, *pb.SearchRecordIndexReq, ...client.Option) (*pb.SearchRecordIndexRsp, error) {
	return nil, errors.New("not used")
}
