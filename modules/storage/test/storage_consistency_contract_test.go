//go:build storage_consistency_contract

package test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	duckdb "github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/primary"
	"github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/search"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This suite is deliberately opt-in. It describes the consistency boundary
// that the storage remediation is expected to make true across the current
// physical stores and the public service facades.
func TestStorageConsistencyContractMergePreservesColumns(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key := &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: "BTC%7C1m%7C", Version: "2026-07-18T00:00:00Z"}
	if err := store.WriteRows(context.Background(), []*pb.PrimaryStoreRow{{Key: key, Columns: []*pb.ColumnValue{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRows(context.Background(), []*pb.PrimaryStoreRow{{Key: key, Columns: []*pb.ColumnValue{{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.ReadRows(context.Background(), []*pb.PrimaryStoreKey{key}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	if err != nil || len(rows) != 1 || len(rows[0].GetColumns()) != 2 {
		t.Fatalf("merged rows=%v err=%v, want one row with two columns", rows, err)
	}
}

func TestStorageConsistencyContractDeleteRemovesFact(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key := &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: "BTC%7C1m%7C", Version: "2026-07-18T00:00:00Z"}
	if err := store.WriteRows(context.Background(), []*pb.PrimaryStoreRow{{Key: key}}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRows(context.Background(), []*pb.PrimaryStoreKey{key}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := store.ReadRows(context.Background(), []*pb.PrimaryStoreKey{key}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows after fact delete=%v err=%v, want empty", rows, err)
	}
}

func TestStorageConsistencyContract(t *testing.T) {
	t.Run("MergeCompleteSnapshots", testMergeCompleteSnapshots)
	t.Run("ViewMergeAndMissingRowRecovery", testViewMergeAndMissingRowRecovery)
	t.Run("OutboxNonContiguousACK", testOutboxNonContiguousACK)
	t.Run("Pagination1001ASCAndDESC", testPagination1001ASCAndDESC)
	t.Run("DeletePropagation", testDeletePropagation)
	t.Run("ViewRangeAndState", testViewRangeAndState)
}

func testMergeCompleteSnapshots(t *testing.T) {
	ctx := context.Background()
	bus := &contractBus{}
	svc := newContractPrimaryStoreService(t, bus)

	firstTime := timeSeriesRow("kline", "BTC-USDT", "2026-07-18T00:00:00Z", map[string]float64{
		"close":    100,
		"momentum": 0.8,
	})
	secondTime := timeSeriesRow("kline", "BTC-USDT", firstTime.GetKey().GetDataTime(), map[string]float64{"close": 101})
	mergeTimeSeries(t, svc, firstTime)
	mergeTimeSeries(t, svc, secondTime)

	readTime, err := svc.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{Keys: []*pb.TimeSeriesKey{firstTime.GetKey()}})
	if err != nil {
		t.Fatalf("ReadTimeSeriesRows: %v", err)
	}
	assertColumnNames(t, "PrimaryStore merged time-series row", readTime.GetRows()[0].GetColumns(), "close", "momentum")
	if len(bus.timeSeries) < 2 {
		t.Fatalf("time-series events=%d, want two", len(bus.timeSeries))
	}
	assertColumnNames(t, "RowsCommitted/archive time-series snapshot", bus.timeSeries[1].GetWrites()[0].GetRow().GetColumns(), "close", "momentum")
	assertColumnNames(t, "Archive time-series snapshot", bus.archiveTimeSeries[0].GetWrites()[0].GetRow().GetColumns(), "close", "momentum")

	version := "2026-07-18T00:00:00Z"
	firstRecord := recordRow("records", "record-1", version, map[string]float64{"score": 0.8}, "first title")
	secondRecord := recordRow("records", "record-1", version, map[string]float64{"score": 0.9}, "")
	mergeRecord(t, svc, firstRecord)
	mergeRecord(t, svc, secondRecord)

	readRecord, err := svc.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{Keys: []*pb.RecordKey{firstRecord.GetKey()}})
	if err != nil {
		t.Fatalf("ReadRecordRows: %v", err)
	}
	assertColumnNames(t, "PrimaryStore merged record row", readRecord.GetRows()[0].GetColumns(), "score", "title")
	if len(bus.records) < 2 {
		t.Fatalf("record events=%d, want two", len(bus.records))
	}
	assertColumnNames(t, "RowsCommitted/archive record snapshot", bus.records[1].GetWrites()[0].GetRow().GetColumns(), "score", "title")
	assertColumnNames(t, "Archive record snapshot", bus.archiveRecords[0].GetWrites()[0].GetRow().GetColumns(), "score", "title")
}

func testViewMergeAndMissingRowRecovery(t *testing.T) {
	t.Run("duckdb", func(t *testing.T) {
		ctx := context.Background()
		root := t.TempDir()
		manager, err := duckdb.OpenIndexManager(duckdb.IndexManagerOptions{Root: root})
		if err != nil {
			if skipNoCGODuckDB(t, err) {
				return
			}
			t.Fatal(err)
		}
		defer manager.Close()
		indexID := viewindex.ViewIndexID("crypto", "kline_view", viewindex.SlotA)
		columns := []*pb.ViewColumn{viewColumn("close"), viewColumn("momentum"), viewColumn("volatility")}
		schema := viewindex.ViewIndexSchema{SpaceID: "crypto", ViewID: "kline_view", Engine: "duckdb", ViewVersion: 1, Columns: columns, SchemaHash: "contract"}
		if err := manager.Prepare(ctx, indexID, schema); err != nil {
			if skipNoCGODuckDB(t, err) {
				return
			}
			t.Fatal(err)
		}
		full := timeSeriesViewRow("2026-07-18T01:00:00Z", 101, 0.8, 1.2)
		patch := timeSeriesViewPatch(full.GetKey().GetDataTime(), 102)
		if err := manager.Write(ctx, indexID, viewindex.ViewIndexBatch{TimeSeriesRows: []*pb.TimeSeriesRow{full}, Columns: columns}); err != nil {
			t.Fatal(err)
		}
		if err := manager.Write(ctx, indexID, viewindex.ViewIndexBatch{TimeSeriesRows: []*pb.TimeSeriesRow{patch}, Columns: columns}); err != nil {
			t.Fatal(err)
		}
		_, rows, _, err := manager.QueryTimeSeriesRows(ctx, indexID, &pb.QueryTimeSeriesRowsReq{SpaceId: "crypto", Page: &pb.Page{Size: 10}})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("merged DuckDB rows=%d, want one", len(rows))
		}
		assertColumnNames(t, "DuckDB MERGE", rows[0].GetColumns(), "close", "momentum", "volatility")

		if err := manager.Remove(ctx, indexID); err != nil {
			t.Fatal(err)
		}
		if err := manager.Prepare(ctx, indexID, schema); err != nil {
			t.Fatal(err)
		}
		if err := manager.Write(ctx, indexID, viewindex.ViewIndexBatch{TimeSeriesRows: []*pb.TimeSeriesRow{patch}, Columns: columns}); err != nil {
			t.Fatal(err)
		}
		_, rows, _, err = manager.QueryTimeSeriesRows(ctx, indexID, &pb.QueryTimeSeriesRowsReq{SpaceId: "crypto", Page: &pb.Page{Size: 10}})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Errorf("missing-row recovery created an incomplete DuckDB row: %d; expected missing RowKey and no write", len(rows))
		}
	})

	t.Run("bleve", func(t *testing.T) {
		ctx := context.Background()
		service := search.NewService(search.Options{Root: t.TempDir()})
		defer service.Close()
		indexID := viewindex.ViewIndexID("crypto", "record_view", viewindex.SlotA)
		columns := []*pb.ViewColumn{viewColumn("score"), viewColumn("momentum"), viewColumn("volatility")}
		schema := viewindex.ViewIndexSchema{SpaceID: "crypto", ViewID: "record_view", Engine: "bleve", ViewVersion: 1, Columns: columns, SchemaHash: "contract"}
		if err := service.Prepare(ctx, indexID, schema); err != nil {
			t.Fatal(err)
		}
		full := recordViewRow("2026-07-18T01:00:00Z", 0.9, 0.8, 1.2)
		patch := recordViewPatch(full.GetKey().GetVersion(), 0.95)
		if err := service.Write(ctx, indexID, viewindex.ViewIndexBatch{RecordRows: []*pb.RecordRow{full}, Columns: columns}); err != nil {
			t.Fatal(err)
		}
		if err := service.Write(ctx, indexID, viewindex.ViewIndexBatch{RecordRows: []*pb.RecordRow{patch}, Columns: columns}); err != nil {
			t.Fatal(err)
		}
		rows, _, err := service.SearchRecordRows(ctx, search.SearchRequest{IndexID: indexID, SpaceID: "crypto", DatasetID: "records", Page: &pb.Page{Size: 10}})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("merged Bleve rows=%d, want one", len(rows))
		}
		assertColumnNames(t, "Bleve MERGE", rows[0].GetColumns(), "score", "momentum", "volatility")

		if err := service.Remove(ctx, indexID); err != nil {
			t.Fatal(err)
		}
		if err := service.Prepare(ctx, indexID, schema); err != nil {
			t.Fatal(err)
		}
		if err := service.Write(ctx, indexID, viewindex.ViewIndexBatch{RecordRows: []*pb.RecordRow{patch}, Columns: columns}); err != nil {
			t.Fatal(err)
		}
		rows, _, err = service.SearchRecordRows(ctx, search.SearchRequest{IndexID: indexID, SpaceID: "crypto", DatasetID: "records", Page: &pb.Page{Size: 10}})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Errorf("missing-row recovery created an incomplete Bleve row: %d; expected missing RowKey and no write", len(rows))
		}
	})
}

func testOutboxNonContiguousACK(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 1; i <= 3; i++ {
		message := contractMessage(fmt.Sprintf("message-%d", i))
		row := &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: "BTC-USDT|1m|_", Version: fmt.Sprintf("2026-07-18T%02d:00:00Z", i)}}
		if err := store.WriteRowsWithOutbox(ctx, []*pb.PrimaryStoreRow{row}, &device.OutboxEntry{Data: message}); err != nil {
			t.Fatal(err)
		}
	}
	publisher := &scriptedPublisher{}
	relay := primary.NewOutboxRelay(store, publisher, primary.OutboxConfig{FlushInterval: 10 * time.Millisecond, BackoffBase: time.Second, BackoffMax: time.Second})
	relay.Start(ctx)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if publisher.calls() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if publisher.calls() == 0 {
		t.Fatal("outbox relay did not attempt a publish")
	}
	_ = relay.Close()
	entries, err := store.ListOutbox(ctx, 0, 10, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Sequence)
	}
	if !equalUint64s(got, []uint64{2, 3}) {
		t.Errorf("outbox after ACK [nil,error,nil] = %v, want [2 3]; relay must delete only the successful prefix", got)
	}
}

func testPagination1001ASCAndDESC(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "primary")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 0; i < 1001; i++ {
		row := &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: "BTC-USDT|1m|_", Version: time.Date(2026, 7, 18, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano)}}
		if err := store.WriteRows(ctx, []*pb.PrimaryStoreRow{row}); err != nil {
			t.Fatal(err)
		}
	}
	for _, order := range []pb.SortOrder{pb.SortOrder_SORT_ORDER_ASC, pb.SortOrder_SORT_ORDER_DESC} {
		for _, size := range []uint32{1, 25, 999} {
			name := fmt.Sprintf("%s-%d", order.String(), size)
			t.Run(name, func(t *testing.T) {
				var versions []string
				cursor := ""
				for {
					rows, page, err := store.ScanRows(ctx, &pb.PrimaryStoreTarget{SpaceId: "crypto", DatasetId: "kline"}, pb.DataKind_DATA_KIND_TIME_SERIES, nil, order, nil, &pb.Page{Size: size, Cursor: cursor})
					if err != nil {
						t.Fatal(err)
					}
					for _, row := range rows {
						versions = append(versions, row.GetKey().GetVersion())
					}
					if !page.GetHasMore() {
						break
					}
					if page.GetNextCursor() == "" || page.GetNextCursor() == cursor {
						t.Fatalf("pagination stopped without advancing cursor: page=%+v cursor=%q", page, cursor)
					}
					cursor = page.GetNextCursor()
				}
				if len(versions) != 1001 {
					t.Fatalf("returned %d rows, want 1001", len(versions))
				}
				unique := make(map[string]bool, len(versions))
				for _, version := range versions {
					unique[version] = true
				}
				if len(unique) != 1001 {
					t.Fatalf("returned %d unique versions, want 1001", len(unique))
				}
			})
		}
	}
}

func testDeletePropagation(t *testing.T) {
	t.Run("duckdb", func(t *testing.T) {
		ctx := context.Background()
		root := t.TempDir()
		store, err := pebble.Open(pebble.Options{Path: filepath.Join(root, "primary")})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		row := timeSeriesViewRow("2026-07-18T02:00:00Z", 101, 0.8, 1.2)
		primaryRow := &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: "BTC-USDT|1m|_", Version: row.GetKey().GetDataTime()}}
		if err := store.WriteRows(ctx, []*pb.PrimaryStoreRow{primaryRow}); err != nil {
			t.Fatal(err)
		}
		manager, err := duckdb.OpenIndexManager(duckdb.IndexManagerOptions{Root: filepath.Join(root, "view")})
		if err != nil {
			if skipNoCGODuckDB(t, err) {
				return
			}
			t.Fatal(err)
		}
		defer manager.Close()
		indexID := viewindex.ViewIndexID("crypto", "kline_view", viewindex.SlotA)
		columns := []*pb.ViewColumn{viewColumn("close"), viewColumn("momentum"), viewColumn("volatility")}
		if err := manager.Prepare(ctx, indexID, viewindex.ViewIndexSchema{SpaceID: "crypto", ViewID: "kline_view", Engine: "duckdb", Columns: columns, SchemaHash: "contract"}); err != nil {
			if skipNoCGODuckDB(t, err) {
				return
			}
			t.Fatal(err)
		}
		if err := manager.Write(ctx, indexID, viewindex.ViewIndexBatch{TimeSeriesRows: []*pb.TimeSeriesRow{row}, Columns: columns}); err != nil {
			t.Fatal(err)
		}
		archive := proto.Clone(row).(*pb.TimeSeriesRow)
		if err := store.DeleteRows(ctx, []*pb.PrimaryStoreKey{primaryRow.GetKey()}); err != nil {
			t.Fatal(err)
		}
		if err := manager.DeleteTimeSeriesRows(ctx, indexID, []*pb.TimeSeriesRow{row}); err != nil {
			t.Fatal(err)
		}
		assertPrimaryDeleted(t, ctx, store, primaryRow.GetKey())
		_, derived, _, err := manager.QueryTimeSeriesRows(ctx, indexID, &pb.QueryTimeSeriesRowsReq{SpaceId: "crypto", Page: &pb.Page{Size: 10}})
		if err != nil {
			t.Fatal(err)
		}
		if len(derived) != 0 {
			t.Errorf("DuckDB retained %d deleted rows; Delete must propagate to derived indexes", len(derived))
		}
		if archive == nil || archive.GetKey().GetDataTime() == "" {
			t.Fatal("archive fixture disappeared; online delete must not delete archived history")
		}
	})

	t.Run("bleve", func(t *testing.T) {
		ctx := context.Background()
		root := t.TempDir()
		store, err := pebble.Open(pebble.Options{Path: filepath.Join(root, "primary")})
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		row := recordViewRow("2026-07-18T02:00:00Z", 0.9, 0.8, 1.2)
		primaryRow := &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "records", DataKind: pb.DataKind_DATA_KIND_RECORD, Key: "record-1", Version: row.GetKey().GetVersion()}}
		if err := store.WriteRows(ctx, []*pb.PrimaryStoreRow{primaryRow}); err != nil {
			t.Fatal(err)
		}
		service := search.NewService(search.Options{Root: filepath.Join(root, "view")})
		defer service.Close()
		indexID := viewindex.ViewIndexID("crypto", "record_view", viewindex.SlotA)
		columns := []*pb.ViewColumn{viewColumn("score"), viewColumn("momentum"), viewColumn("volatility")}
		if err := service.Prepare(ctx, indexID, viewindex.ViewIndexSchema{SpaceID: "crypto", ViewID: "record_view", Engine: "bleve", Columns: columns, SchemaHash: "contract"}); err != nil {
			t.Fatal(err)
		}
		if err := service.Write(ctx, indexID, viewindex.ViewIndexBatch{RecordRows: []*pb.RecordRow{row}, Columns: columns}); err != nil {
			t.Fatal(err)
		}
		archive := proto.Clone(row).(*pb.RecordRow)
		if err := store.DeleteRows(ctx, []*pb.PrimaryStoreKey{primaryRow.GetKey()}); err != nil {
			t.Fatal(err)
		}
		if err := service.DeleteRecordRows(ctx, indexID, []*pb.RecordRow{row}); err != nil {
			t.Fatal(err)
		}
		assertPrimaryDeleted(t, ctx, store, primaryRow.GetKey())
		derived, _, err := service.SearchRecordRows(ctx, search.SearchRequest{IndexID: indexID, SpaceID: "crypto", DatasetID: "records", Page: &pb.Page{Size: 10}})
		if err != nil {
			t.Fatal(err)
		}
		if len(derived) != 0 {
			t.Errorf("Bleve retained %d deleted rows; Delete must propagate to derived indexes", len(derived))
		}
		if archive == nil || archive.GetKey().GetRecordId() == "" {
			t.Fatal("archive fixture disappeared; online delete must not delete archived history")
		}
	})
}

func assertPrimaryDeleted(t *testing.T, ctx context.Context, store *pebble.Store, key *pb.PrimaryStoreKey) {
	t.Helper()
	facts, _, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{key}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, &pb.Page{Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 0 {
		t.Fatalf("PrimaryStore returned %d rows after delete", len(facts))
	}
}

func testViewRangeAndState(t *testing.T) {
	baseView := &pb.View{
		SpaceId: "crypto", ViewId: "kline_view", PrimaryDatasetId: "kline", DatasetIds: []string{"kline"},
		Engine: "duckdb", Status: "active", ActiveIndexId: "index-a", ViewVersion: 1, ActiveViewVersion: 1,
		IndexedFrom: "2026-07-18T10:00:00Z", IndexedTo: "2026-07-18T11:00:00Z",
		Attributes: map[string]string{"shard_head_sequence": "10", "checkpoint_sequence": "9"},
	}
	metadata := &contractViewMetadata{view: baseView, dataset: &pb.Dataset{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active"}}
	indexes := &contractViewIndex{}
	svc := view.NewService(view.ServiceOptions{Metadata: metadata, TimeSeriesIndexes: indexes})
	cases := []struct {
		name   string
		mutate func(*pb.View, *pb.Dataset)
		range_ *pb.TimeRange
	}{
		{name: "before-indexed-from", range_: &pb.TimeRange{StartTime: "2026-07-18T09:00:00Z", EndTime: "2026-07-18T09:30:00Z"}},
		{name: "after-indexed-to", range_: &pb.TimeRange{StartTime: "2026-07-18T11:30:00Z", EndTime: "2026-07-18T12:00:00Z"}},
		{name: "inactive-view", mutate: func(v *pb.View, _ *pb.Dataset) { v.Status = "inactive" }},
		{name: "inactive-dataset", mutate: func(_ *pb.View, d *pb.Dataset) { d.Status = "disabled" }},
		{name: "checkpoint-behind-shard-head", mutate: func(v *pb.View, _ *pb.Dataset) {
			v.Attributes["checkpoint_sequence"] = "9"
			v.Attributes["shard_head_sequence"] = "10"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata.view = proto.Clone(baseView).(*pb.View)
			metadata.dataset = proto.Clone(metadata.dataset).(*pb.Dataset)
			if tc.mutate != nil {
				tc.mutate(metadata.view, metadata.dataset)
			}
			req := &pb.QueryTimeSeriesRowsReq{SpaceId: "crypto", ViewId: "kline_view", TimeRange: tc.range_, Page: &pb.Page{Size: 10}}
			resp, err := svc.QueryTimeSeriesRows(context.Background(), req)
			if err != nil {
				t.Fatalf("QueryTimeSeriesRows transport error: %v", err)
			}
			if resp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
				t.Errorf("range/state violation returned ordinary success: %+v", resp.GetRetInfo())
			}
		})
	}
}

type contractBus struct {
	mu                sync.Mutex
	timeSeries        []*pb.TimeSeriesRowsCommitted
	records           []*pb.RecordRowsCommitted
	archiveTimeSeries []*pb.TimeSeriesRowsCommitted
	archiveRecords    []*pb.RecordRowsCommitted
}

func (b *contractBus) PublishTimeSeriesRowsCommitted(_ context.Context, event *pb.TimeSeriesRowsCommitted) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.timeSeries = append(b.timeSeries, proto.Clone(event).(*pb.TimeSeriesRowsCommitted))
	b.archiveTimeSeries = append(b.archiveTimeSeries, proto.Clone(event).(*pb.TimeSeriesRowsCommitted))
	return nil
}

func (b *contractBus) PublishRecordRowsCommitted(_ context.Context, event *pb.RecordRowsCommitted) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.records = append(b.records, proto.Clone(event).(*pb.RecordRowsCommitted))
	b.archiveRecords = append(b.archiveRecords, proto.Clone(event).(*pb.RecordRowsCommitted))
	return nil
}

type contractPrimary struct {
	local *primary.LocalClient
	bus   *contractBus
}

func (p *contractPrimary) WriteRows(ctx context.Context, target *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow) error {
	if err := p.local.WriteRows(ctx, target, rows); err != nil {
		return err
	}
	return p.publishLatest(ctx)
}
func (p *contractPrimary) WriteRowsWithMessage(ctx context.Context, target *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow, _ []byte) error {
	if err := p.local.WriteRows(ctx, target, rows); err != nil {
		return err
	}
	return p.publishLatest(ctx)
}
func (p *contractPrimary) ReadRows(ctx context.Context, target *pb.PrimaryStoreTarget, req *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return p.local.ReadRows(ctx, target, req)
}
func (p *contractPrimary) ScanRows(ctx context.Context, target *pb.PrimaryStoreTarget, req *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return p.local.ScanRows(ctx, target, req)
}
func (p *contractPrimary) DeleteRows(ctx context.Context, target *pb.PrimaryStoreTarget, keys []*pb.PrimaryStoreKey) error {
	if err := p.local.DeleteRows(ctx, target, keys); err != nil {
		return err
	}
	return p.publishLatest(ctx)
}
func (p *contractPrimary) Close() error { return p.local.Close() }

func (p *contractPrimary) publishLatest(ctx context.Context) error {
	entries, err := p.local.ListOutbox(ctx, 0, 100, 1<<20)
	if err != nil || len(entries) == 0 {
		return err
	}
	msg := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(entries[len(entries)-1].Data, msg); err != nil {
		return err
	}
	switch msg.GetMessageType() {
	case "moox.storage.time_series.rows_committed.v1":
		event := &pb.TimeSeriesRowsCommitted{}
		if err := proto.Unmarshal(msg.GetPayload(), event); err != nil {
			return err
		}
		return p.bus.PublishTimeSeriesRowsCommitted(ctx, event)
	case "moox.storage.record.rows_committed.v1":
		event := &pb.RecordRowsCommitted{}
		if err := proto.Unmarshal(msg.GetPayload(), event); err != nil {
			return err
		}
		return p.bus.PublishRecordRowsCommitted(ctx, event)
	default:
		return fmt.Errorf("unexpected committed message type %q", msg.GetMessageType())
	}
}

func newContractPrimaryStoreService(t *testing.T, bus *contractBus) *primarystore.Service {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	local := primary.NewLocalClient(primary.LocalClientOptions{PebblePath: filepath.Join(root, "pebble"), ShardID: "node-1"})
	svc := primarystore.NewServiceWithOptions(primarystore.Options{Root: root, InitSchemaPath: storageSchemaPath(), PrimaryClient: &contractPrimary{local: local, bus: bus}, Events: bus})
	t.Cleanup(func() { _ = svc.Close() })
	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}})
	mustContractOK(t, spaceRsp, err)
	nodeRsp, err := svc.CreatePrimaryStoreNode(ctx, &pb.CreatePrimaryStoreNodeReq{Node: &pb.PrimaryStoreNode{NodeId: "node-1", Name: "Node 1", Status: "active"}})
	mustContractOK(t, nodeRsp, err)
	deviceRsp, err := svc.CreateDevice(ctx, &pb.CreateDeviceReq{Device: &pb.Device{DeviceId: "device-1", NodeId: "node-1", Engine: "pebble", Status: "active"}})
	mustContractOK(t, deviceRsp, err)
	sourceRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: "crypto", DataSourceId: "source", Name: "Source", Kind: "internal", Status: "active"}})
	mustContractOK(t, sourceRsp, err)
	for _, dataset := range []*pb.Dataset{
		{SpaceId: "crypto", DatasetId: "kline", DataSourceId: "source", Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"},
		{SpaceId: "crypto", DatasetId: "records", DataSourceId: "source", Name: "记录", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"},
	} {
		datasetRsp, err := svc.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: dataset})
		mustContractOK(t, datasetRsp, err)
		routeRsp, err := svc.CreatePrimaryStoreRoute(ctx, &pb.CreatePrimaryStoreRouteReq{PrimaryStoreRoute: &pb.PrimaryStoreRoute{SpaceId: "crypto", DatasetId: dataset.GetDatasetId(), NodeId: "node-1", Status: "active"}})
		mustContractOK(t, routeRsp, err)
	}
	for _, column := range []*pb.DatasetColumn{
		{SpaceId: "crypto", DatasetId: "kline", ColumnName: "close", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active", Attributes: map[string]string{"display_name": "收盘"}},
		{SpaceId: "crypto", DatasetId: "kline", ColumnName: "momentum", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR, OriginId: "momentum", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active", Attributes: map[string]string{"display_name": "动量"}},
		{SpaceId: "crypto", DatasetId: "records", ColumnName: "score", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: "score", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active", Attributes: map[string]string{"display_name": "评分"}},
		{SpaceId: "crypto", DatasetId: "records", ColumnName: "title", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active", Attributes: map[string]string{"display_name": "标题"}},
	} {
		columnRsp, err := svc.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: column})
		mustContractOK(t, columnRsp, err)
	}
	return svc
}

func storageSchemaPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "schema", "metadata.sql")
}

func mustContractOK(t *testing.T, result interface{ GetRetInfo() *pb.RetInfo }, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("RPC transport error: %v", err)
	}
	if result.GetRetInfo() == nil || result.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("RPC failed: %+v", result.GetRetInfo())
	}
}

func mergeTimeSeries(t *testing.T, svc *primarystore.Service, row *pb.TimeSeriesRow) {
	t.Helper()
	resp, err := svc.MergeTimeSeriesRows(context.Background(), &pb.MergeTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{row}})
	mustContractOK(t, resp, err)
}

func mergeRecord(t *testing.T, svc *primarystore.Service, row *pb.RecordRow) {
	t.Helper()
	resp, err := svc.MergeRecordRows(context.Background(), &pb.MergeRecordRowsReq{Rows: []*pb.RecordRow{row}})
	mustContractOK(t, resp, err)
}

func timeSeriesRow(dataset, subject, dataTime string, values map[string]float64) *pb.TimeSeriesRow {
	columns := make([]*pb.ColumnValue, 0, len(values))
	for name, value := range values {
		columns = append(columns, &pb.ColumnValue{ColumnName: name, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}})
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].GetColumnName() < columns[j].GetColumnName() })
	return &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: dataset, SubjectId: subject, Freq: "1m", DataTime: dataTime}, Columns: columns}
}

func recordRow(dataset, recordID, version string, values map[string]float64, title string) *pb.RecordRow {
	columns := make([]*pb.ColumnValue, 0, len(values)+1)
	for name, value := range values {
		columns = append(columns, &pb.ColumnValue{ColumnName: name, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}})
	}
	if title != "" {
		columns = append(columns, &pb.ColumnValue{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: title}}})
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].GetColumnName() < columns[j].GetColumnName() })
	return &pb.RecordRow{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: dataset, RecordId: recordID, Version: version}, Columns: columns}
}

func timeSeriesViewRow(dataTime string, close, momentum, volatility float64) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: dataTime}, Columns: []*pb.ColumnValue{doubleColumn("close", close), doubleColumn("momentum", momentum), doubleColumn("volatility", volatility)}}
}

func timeSeriesViewPatch(dataTime string, close float64) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: dataTime}, Columns: []*pb.ColumnValue{doubleColumn("close", close)}}
}

func recordViewRow(version string, score, momentum, volatility float64) *pb.RecordRow {
	return &pb.RecordRow{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "records", RecordId: "record-1", Version: version}, Columns: []*pb.ColumnValue{doubleColumn("score", score), doubleColumn("momentum", momentum), doubleColumn("volatility", volatility)}}
}

func recordViewPatch(version string, score float64) *pb.RecordRow {
	return &pb.RecordRow{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "records", RecordId: "record-1", Version: version}, Columns: []*pb.ColumnValue{doubleColumn("score", score)}}
}

func doubleColumn(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{ColumnName: name, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func viewColumn(name string) *pb.ViewColumn {
	return &pb.ViewColumn{SpaceId: "crypto", ViewId: "view", ColumnName: name, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "dataset." + name, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}
}

func assertColumnNames(t *testing.T, label string, columns []*pb.ColumnValue, want ...string) {
	t.Helper()
	got := make([]string, 0, len(columns))
	for _, column := range columns {
		got = append(got, column.GetColumnName())
	}
	sort.Strings(got)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s columns=%v, want %v", label, got, want)
	}
}

func contractMessage(id string) []byte {
	now := timestamppb.Now()
	payload, _ := proto.Marshal(&pb.TimeSeriesRowsCommitted{ShardId: "node-1", SpaceId: "crypto", DatasetId: "kline"})
	raw, _ := proto.Marshal(&messagepb.MooxMessage{ProtocolVersion: jetstream.ProtocolVersion, MessageId: id, Topic: "moox.storage.rows_committed.time_series.v1.mzxw6", Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, Producer: &messagepb.Producer{ServiceName: "storage", InstanceId: "node-1"}, Sequence: 1, OccurredAt: now, PublishedAt: now, ContentType: "application/x-protobuf", MessageType: "moox.storage.time_series.rows_committed.v1", Payload: payload})
	return raw
}

func skipNoCGODuckDB(t *testing.T, err error) bool {
	t.Helper()
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "cgo") || strings.Contains(message, "duckdb") && strings.Contains(message, "requires") {
		t.Skipf("DuckDB contract requires cgo: %v", err)
		return true
	}
	return false
}

type scriptedPublisher struct {
	mu    sync.Mutex
	count int
}

func (p *scriptedPublisher) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}
func (p *scriptedPublisher) PublishMessage(_ context.Context, message []byte) error {
	p.mu.Lock()
	p.count++
	call := p.count
	p.mu.Unlock()
	if call == 2 {
		return errors.New("contract publish failure")
	}
	_ = message
	return nil
}

type contractViewMetadata struct {
	view    *pb.View
	dataset *pb.Dataset
}

func (m *contractViewMetadata) GetView(context.Context, string, string) (*pb.View, error) {
	return proto.Clone(m.view).(*pb.View), nil
}
func (m *contractViewMetadata) ListViewColumns(context.Context, string, string, *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	return m.view.GetActiveColumns(), &pb.PageResult{}, nil
}
func (m *contractViewMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return proto.Clone(m.dataset).(*pb.Dataset), nil
}

type contractViewIndex struct{}

func (*contractViewIndex) QueryTimeSeriesRows(context.Context, string, *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error) {
	return nil, []*pb.TimeSeriesRow{{}}, &pb.PageResult{Page: 1, Size: 10}, nil
}

func equalUint64s(got, want []uint64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
