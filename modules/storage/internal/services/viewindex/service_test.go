package viewindex

import (
	"context"
	"errors"
	"testing"

	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestServiceRoutesLifecycleByEngine(t *testing.T) {
	duck := &fakeManagedEngine{name: "duckdb"}
	bleve := &fakeManagedEngine{name: "bleve"}
	service := NewService(Options{
		Engines:    map[string]ManagedEngine{"duckdb": duck, "bleve": bleve},
		TimeSeries: duck,
		Records:    bleve,
	})
	indexID := coreviewindex.ViewIndexID("crypto", "spot", coreviewindex.SlotA)
	rsp, err := service.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
		IndexId: indexID,
		Engine:  "duckdb",
		Schema: &pb.ViewIndexSchema{
			SpaceId: "crypto", ViewId: "spot", ViewVersion: 1, Engine: "duckdb", SchemaHash: "schema-1",
		},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("PrepareViewIndex rsp=%+v err=%v", rsp, err)
	}
	if duck.prepared != indexID || bleve.prepared != "" {
		t.Fatalf("prepared duck=%q bleve=%q", duck.prepared, bleve.prepared)
	}
	writeRsp, err := service.WriteViewIndex(context.Background(), &pb.WriteViewIndexReq{
		IndexId: indexID, Engine: "duckdb", Batch: &pb.ViewIndexBatch{ViewVersion: 1, SchemaHash: "schema-1"},
	})
	if err != nil || writeRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("fenced write rsp=%+v err=%v", writeRsp, err)
	}
	staleRsp, err := service.WriteViewIndex(context.Background(), &pb.WriteViewIndexReq{
		IndexId: indexID, Engine: "duckdb", Batch: &pb.ViewIndexBatch{ViewVersion: 2, SchemaHash: "schema-1"},
	})
	if err != nil || staleRsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		t.Fatalf("stale write rsp=%+v err=%v", staleRsp, err)
	}

	bad, err := service.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
		IndexId: indexID,
		Engine:  "bleve",
		Schema:  &pb.ViewIndexSchema{SpaceId: "crypto", ViewId: "spot", Engine: "duckdb"},
	})
	if err != nil || bad.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		t.Fatalf("engine mismatch rsp=%+v err=%v", bad, err)
	}
}

func TestServiceRoutesTypedQueries(t *testing.T) {
	duck := &fakeManagedEngine{name: "duckdb"}
	bleve := &fakeManagedEngine{name: "bleve"}
	service := NewService(Options{
		Engines:    map[string]ManagedEngine{"duckdb": duck, "bleve": bleve},
		TimeSeries: duck,
		Records:    bleve,
	})
	indexID := coreviewindex.ViewIndexID("crypto", "spot", coreviewindex.SlotA)
	timeRsp, err := service.QueryTimeSeriesIndex(context.Background(), &pb.QueryTimeSeriesIndexReq{
		IndexId: indexID,
		Query:   &pb.QueryTimeSeriesRowsReq{SpaceId: "crypto", Page: &pb.Page{Page: 1, Size: 25}},
	})
	if err != nil || len(timeRsp.GetRows()) != 1 || duck.timeSeriesQueries != 1 {
		t.Fatalf("time query rsp=%+v calls=%d err=%v", timeRsp, duck.timeSeriesQueries, err)
	}
	recordRsp, err := service.SearchRecordIndex(context.Background(), &pb.SearchRecordIndexReq{
		IndexId:   indexID,
		DatasetId: "news",
		Query:     &pb.SearchRecordRowsReq{SpaceId: "crypto", Page: &pb.Page{Page: 1, Size: 25}},
	})
	if err != nil || len(recordRsp.GetRows()) != 1 || bleve.recordQueries != 1 {
		t.Fatalf("record query rsp=%+v calls=%d err=%v", recordRsp, bleve.recordQueries, err)
	}
}

func TestServiceMapsEngineErrorsToRetInfo(t *testing.T) {
	duck := &fakeManagedEngine{name: "duckdb", err: errors.New("disk full")}
	service := NewService(Options{Engines: map[string]ManagedEngine{"duckdb": duck}, TimeSeries: duck})
	indexID := coreviewindex.ViewIndexID("crypto", "spot", coreviewindex.SlotA)
	rsp, err := service.StatViewIndex(context.Background(), &pb.StatViewIndexReq{IndexId: indexID, Engine: "duckdb"})
	if err != nil {
		t.Fatalf("StatViewIndex transport error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS || rsp.GetRetInfo().GetMsg() == "" {
		t.Fatalf("StatViewIndex rsp = %+v", rsp)
	}
}

func TestServiceListsIndexesWithoutOpeningEveryDatabaseByDefault(t *testing.T) {
	duck := &fakeManagedEngine{name: "duckdb"}
	service := NewService(Options{Engines: map[string]ManagedEngine{"duckdb": duck}, TimeSeries: duck})

	rsp, err := service.ListViewIndexes(context.Background(), &pb.ListViewIndexesReq{Engine: "duckdb"})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetIndexes()) != 1 {
		t.Fatalf("ListViewIndexes rsp=%+v err=%v", rsp, err)
	}
	if duck.statCalls != 0 || rsp.GetIndexes()[0].GetStats() != nil {
		t.Fatalf("default list stat_calls=%d descriptor=%+v", duck.statCalls, rsp.GetIndexes()[0])
	}
	withStats, err := service.ListViewIndexes(context.Background(), &pb.ListViewIndexesReq{Engine: "duckdb", IncludeStats: true})
	if err != nil || withStats.GetIndexes()[0].GetStats() == nil || duck.statCalls != 1 {
		t.Fatalf("include stats rsp=%+v stat_calls=%d err=%v", withStats, duck.statCalls, err)
	}
}

type fakeManagedEngine struct {
	name              string
	err               error
	prepared          string
	timeSeriesQueries int
	recordQueries     int
	statCalls         int
}

func (e *fakeManagedEngine) Engine() string { return e.name }

func (e *fakeManagedEngine) Prepare(_ context.Context, indexID string, _ coreviewindex.ViewIndexSchema) error {
	e.prepared = indexID
	return e.err
}

func (e *fakeManagedEngine) Write(context.Context, string, coreviewindex.ViewIndexBatch) error {
	return e.err
}

func (e *fakeManagedEngine) Stat(context.Context, string) (coreviewindex.ViewIndexStats, error) {
	e.statCalls++
	return coreviewindex.ViewIndexStats{Exists: e.err == nil, EntryCount: 1}, e.err
}

func (e *fakeManagedEngine) Remove(context.Context, string) error { return e.err }

func (e *fakeManagedEngine) List(context.Context) ([]string, error) {
	return []string{coreviewindex.ViewIndexID("crypto", "spot", coreviewindex.SlotA)}, e.err
}

func (e *fakeManagedEngine) QueryTimeSeriesRows(context.Context, string, *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error) {
	e.timeSeriesQueries++
	return nil, []*pb.TimeSeriesRow{{}}, &pb.PageResult{Page: 1, Size: 25}, e.err
}

func (e *fakeManagedEngine) QueryRecordRows(context.Context, string, string, *pb.SearchRecordRowsReq) ([]*pb.ResultColumn, []*pb.RecordRow, *pb.PageResult, error) {
	e.recordQueries++
	return nil, []*pb.RecordRow{{}}, &pb.PageResult{Page: 1, Size: 25}, e.err
}
