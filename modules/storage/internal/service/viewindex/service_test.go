package viewindex

import (
	"context"
	"errors"
	"testing"

	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
			SpaceId: "crypto", ViewId: "spot", ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema-1",
		},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("PrepareViewIndex rsp=%+v err=%v", rsp, err)
	}
	if duck.prepared != indexID || bleve.prepared != "" {
		t.Fatalf("prepared duck=%q bleve=%q", duck.prepared, bleve.prepared)
	}
	rowWrite := &pb.ViewIndexRowWrite{Operation: pb.ViewIndexRowWriteOperation_VIEW_INDEX_ROW_WRITE_OPERATION_MERGE, Key: &pb.ViewIndexRowKey{Key: &pb.ViewIndexRowKey_TimeSeriesKey{TimeSeriesKey: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "spot", SubjectId: "BTC", Freq: "1m", DataTime: "2026-01-01T00:00:00Z"}}}}
	writeRsp, err := service.ApplyViewIndex(context.Background(), &pb.ApplyViewIndexReq{
		IndexId: indexID, Engine: "duckdb", Batch: &pb.ViewIndexApplyBatch{ViewVersion: 1, ViewSchemaHash: "schema-1", RowWrites: []*pb.ViewIndexRowWrite{rowWrite}},
	})
	if err != nil || writeRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("fenced write rsp=%+v err=%v", writeRsp, err)
	}
	staleRsp, err := service.ApplyViewIndex(context.Background(), &pb.ApplyViewIndexReq{
		IndexId: indexID, Engine: "duckdb", Batch: &pb.ViewIndexApplyBatch{ViewVersion: 2, ViewSchemaHash: "schema-1", RowWrites: []*pb.ViewIndexRowWrite{rowWrite}},
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

func TestServiceRestartRejectsStaleViewVersionWithSameSchemaHash(t *testing.T) {
	duck := &fakeManagedEngine{name: "duckdb"}
	indexID := coreviewindex.ViewIndexID("crypto", "spot", coreviewindex.SlotA)
	first := NewService(Options{Engines: map[string]ManagedEngine{"duckdb": duck}})
	prepared, err := first.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
		IndexId: indexID,
		Engine:  "duckdb",
		Schema: &pb.ViewIndexSchema{
			SpaceId: "crypto", ViewId: "spot", ViewVersion: 2, Engine: "duckdb", ViewSchemaHash: "unchanged-shape",
		},
	})
	if err != nil || prepared.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("PrepareViewIndex rsp=%+v err=%v", prepared, err)
	}

	restarted := NewService(Options{Engines: map[string]ManagedEngine{"duckdb": duck}})
	stale, err := restarted.ApplyViewIndex(context.Background(), &pb.ApplyViewIndexReq{
		IndexId: indexID,
		Engine:  "duckdb",
		Batch:   &pb.ViewIndexApplyBatch{ViewVersion: 1, ViewSchemaHash: "unchanged-shape"},
	})
	if err != nil {
		t.Fatalf("ApplyViewIndex transport error: %v", err)
	}
	if stale.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		t.Fatalf("stale write accepted after owner restart: %+v", stale)
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
	viewVersion       uint64
	schemaHash        string
}

func (e *fakeManagedEngine) Engine() string { return e.name }

func (e *fakeManagedEngine) Prepare(_ context.Context, indexID string, schema coreviewindex.ViewIndexSchema) error {
	e.prepared = indexID
	e.viewVersion = schema.ViewVersion
	e.schemaHash = schema.SchemaHash
	return e.err
}

func (e *fakeManagedEngine) Write(context.Context, string, coreviewindex.ViewIndexBatch) error {
	return e.err
}

func (e *fakeManagedEngine) Apply(context.Context, string, coreviewindex.ViewIndexApplyBatch) error {
	return e.err
}

func (e *fakeManagedEngine) Stat(context.Context, string) (coreviewindex.ViewIndexStats, error) {
	e.statCalls++
	return coreviewindex.ViewIndexStats{Exists: e.err == nil, ViewVersion: e.viewVersion, EntryCount: 1, SchemaHash: e.schemaHash}, e.err
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
