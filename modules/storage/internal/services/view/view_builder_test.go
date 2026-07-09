package view

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// fakeFactReader is a Task 7 FactReader test double. It serves TimeSeries
// rows for a single dataset from an in-memory slice, supports cursor-based
// pagination via pageSize, and records the TimeRange passed to
// ScanTimeSeriesRows so tests can assert the effective backfill window.
type fakeFactReader struct {
	mu         sync.Mutex
	rowsByDS   map[string][]*pb.TimeSeriesRow
	pageSize   int // 0 = serve all remaining rows in a single page
	scanCalls  int
	lastRanges []*pb.TimeRange
}

func newFakeFactReader() *fakeFactReader {
	return &fakeFactReader{rowsByDS: map[string][]*pb.TimeSeriesRow{}}
}

func (f *fakeFactReader) setRows(datasetID string, rows []*pb.TimeSeriesRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rowsByDS[datasetID] = rows
}

func (f *fakeFactReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*pb.TimeSeriesRow
	for _, key := range req.GetKeys() {
		for _, row := range f.rowsByDS[key.GetDatasetId()] {
			if row.GetKey().GetSubjectId() == key.GetSubjectId() && row.GetKey().GetDataTime() == key.GetDataTime() {
				out = append(out, row)
			}
		}
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Rows: out}, nil
}

func (f *fakeFactReader) ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanCalls++
	f.lastRanges = append(f.lastRanges, timeRange)
	rows := f.rowsByDS[datasetID]
	pageRows, result := paginate(len(rows), f.pageSize, page.GetCursor())
	out := make([]*pb.TimeSeriesRow, 0, len(pageRows))
	for _, idx := range pageRows {
		out = append(out, rows[idx])
	}
	return out, result, nil
}

// fakeRecordFactReader is a Task 7 RecordFactReader test double, mirroring
// fakeFactReader for Record rows and VersionRange assertions.
type fakeRecordFactReader struct {
	mu         sync.Mutex
	rowsByDS   map[string][]*pb.RecordRow
	pageSize   int
	scanCalls  int
	lastRanges []*pb.VersionRange
}

func newFakeRecordFactReader() *fakeRecordFactReader {
	return &fakeRecordFactReader{rowsByDS: map[string][]*pb.RecordRow{}}
}

func (f *fakeRecordFactReader) setRows(datasetID string, rows []*pb.RecordRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rowsByDS[datasetID] = rows
}

func (f *fakeRecordFactReader) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*pb.RecordRow
	for _, key := range req.GetKeys() {
		for _, row := range f.rowsByDS[key.GetDatasetId()] {
			if row.GetKey().GetRecordId() == key.GetRecordId() {
				out = append(out, row)
			}
		}
	}
	return &pb.ReadRecordRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Rows: out}, nil
}

func (f *fakeRecordFactReader) ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanCalls++
	f.lastRanges = append(f.lastRanges, versionRange)
	rows := f.rowsByDS[datasetID]
	pageRows, result := paginate(len(rows), f.pageSize, page.GetCursor())
	out := make([]*pb.RecordRow, 0, len(pageRows))
	for _, idx := range pageRows {
		out = append(out, rows[idx])
	}
	return out, result, nil
}

// paginate returns the [offset, end) index range for a page and the
// PageResult describing whether more pages remain. pageSize<=0 means serve
// every remaining row in a single page.
func paginate(total int, pageSize int, cursor string) ([]int, *pb.PageResult) {
	offset := 0
	if cursor != "" {
		offset, _ = strconv.Atoi(cursor)
	}
	size := pageSize
	if size <= 0 {
		size = total
	}
	end := offset + size
	hasMore := end < total
	if end > total {
		end = total
	}
	indexes := make([]int, 0, end-offset)
	for i := offset; i < end; i++ {
		indexes = append(indexes, i)
	}
	result := &pb.PageResult{HasMore: hasMore}
	if hasMore {
		result.NextCursor = strconv.Itoa(end)
	}
	return indexes, result
}

func tsRow(datasetID string, subjectID string, dataTime string) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{DatasetId: datasetID, SubjectId: subjectID, DataTime: dataTime}}
}

func recordRow(datasetID string, recordID string, version string) *pb.RecordRow {
	return &pb.RecordRow{Key: &pb.RecordKey{DatasetId: datasetID, RecordId: recordID, Version: version}}
}

// TestBackfillTimeSeriesWindowUsesFreqBackfillWindow proves a TimeSeries
// View whose Dataset has Freqs 1m+1d backfills the daily window from
// rotation.time_series.freq_backfill_window["1d"], not just
// overlap_window/default_backfill_window.
func TestBackfillTimeSeriesWindowUsesFreqBackfillWindow(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID, datasetID := "space1", "view_daily", "ds1"
	fixedNow := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb", PrimaryDatasetId: datasetID,
		ViewVersion: 1, BuildingViewVersion: 1, BuildingResult: "idx_b", BuildStatus: "building",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())
	metadata.putDataset(&pb.Dataset{
		SpaceId: spaceID, DatasetId: datasetID, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
		Freqs: []string{"1m", "1d"},
	})

	facts := newFakeFactReader()
	facts.setRows(datasetID, []*pb.TimeSeriesRow{tsRow(datasetID, "sub1", "2026-07-09T00:00:00Z")})

	cfg := RotationConfig{
		DefaultBackfillWindow: 24 * time.Hour,
		OverlapWindow:         30 * time.Minute,
		TimeSeriesFreqBackfillWindow: map[string]time.Duration{
			"1m": 6 * time.Hour,
			"1d": 730 * 24 * time.Hour,
		},
	}
	backfill := NewPrimaryStoreBackfill(PrimaryStoreBackfillOptions{
		Metadata: metadata, Facts: facts, Config: cfg, Now: func() time.Time { return fixedNow },
	})

	view, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	engine := newFakeEngine("duckdb")
	done, err := backfill(ctx, engine, view, "idx_b")
	if err != nil {
		t.Fatalf("backfill returned error: %v", err)
	}
	if !done {
		t.Fatalf("expected backfill to report done=true after scanning the single page")
	}
	if len(facts.lastRanges) == 0 {
		t.Fatalf("expected ScanTimeSeriesRows to be called with a time range")
	}
	got, err := time.Parse(time.RFC3339, facts.lastRanges[0].GetStartTime())
	if err != nil {
		t.Fatalf("parse start time: %v", err)
	}
	want := fixedNow.Add(-730 * 24 * time.Hour)
	if diff := got.Sub(want); diff < -time.Minute || diff > time.Minute {
		t.Fatalf("backfill start time = %v, want ~%v (freq_backfill_window[1d] must dominate default_backfill_window and overlap_window)", got, want)
	}
	if engine.writeCalls == 0 {
		t.Fatalf("expected the scanned row to be written to the warming index")
	}
}

// TestBackfillRecordWindowUsesDefaultVersionWindow proves a Record View
// backfill uses VersionRange{StartVersion: now - default_version_window,
// EndVersion: now} when record versions are timestamp-like.
func TestBackfillRecordWindowUsesDefaultVersionWindow(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID, datasetID := "space1", "view_record_window", "ds1"
	fixedNow := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "bleve", PrimaryDatasetId: datasetID,
		ViewVersion: 1, BuildingViewVersion: 1, BuildingResult: "idx_b", BuildStatus: "building",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	records := newFakeRecordFactReader()
	records.setRows(datasetID, []*pb.RecordRow{recordRow(datasetID, "rec1", "2026-07-09T00:00:00Z")})

	cfg := RotationConfig{
		RecordDefaultVersionWindow: 30 * 24 * time.Hour,
		OverlapWindow:              30 * time.Minute,
		RecordMaxBackfillEntries:   1000,
	}
	backfill := NewPrimaryStoreBackfill(PrimaryStoreBackfillOptions{
		Metadata: metadata, Records: records, Config: cfg, Now: func() time.Time { return fixedNow },
	})

	view, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	engine := newFakeEngine("bleve")
	done, err := backfill(ctx, engine, view, "idx_b")
	if err != nil {
		t.Fatalf("backfill returned error: %v", err)
	}
	if !done {
		t.Fatalf("expected backfill to report done=true after scanning the single page")
	}
	if len(records.lastRanges) == 0 {
		t.Fatalf("expected ScanRecordRows to be called with a version range")
	}
	got, err := time.Parse(time.RFC3339, records.lastRanges[0].GetStartVersion())
	if err != nil {
		t.Fatalf("parse start version: %v", err)
	}
	want := fixedNow.Add(-30 * 24 * time.Hour)
	if diff := got.Sub(want); diff < -time.Minute || diff > time.Minute {
		t.Fatalf("backfill start version = %v, want ~%v (record.default_version_window)", got, want)
	}
	if engine.writeCalls == 0 {
		t.Fatalf("expected the scanned record row to be written to the warming index")
	}
}

// TestBackfillRecordCapsScanAtMaxBackfillEntriesForNonTimestampVersions
// proves non-timestamp Record versions are bounded by
// record.max_backfill_entries plus a warning, instead of scanning the full
// Record history.
func TestBackfillRecordCapsScanAtMaxBackfillEntriesForNonTimestampVersions(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID, datasetID := "space1", "view_record_cap", "ds1"

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "bleve", PrimaryDatasetId: datasetID,
		ViewVersion: 1, BuildingViewVersion: 1, BuildingResult: "idx_b", BuildStatus: "building",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	records := newFakeRecordFactReader()
	records.pageSize = 2
	// Opaque record revision IDs: not timestamp-like, so a version-range
	// filter cannot reliably bound the scan.
	records.setRows(datasetID, []*pb.RecordRow{
		recordRow(datasetID, "rec1", "rev-001"),
		recordRow(datasetID, "rec2", "rev-002"),
		recordRow(datasetID, "rec3", "rev-003"),
		recordRow(datasetID, "rec4", "rev-004"),
		recordRow(datasetID, "rec5", "rev-005"),
	})

	cfg := RotationConfig{RecordDefaultVersionWindow: 30 * 24 * time.Hour, RecordMaxBackfillEntries: 3}
	backfill := NewPrimaryStoreBackfill(PrimaryStoreBackfillOptions{Metadata: metadata, Records: records, Config: cfg})

	view, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	engine := newFakeEngine("bleve")
	done, err := backfill(ctx, engine, view, "idx_b")
	if err != nil {
		t.Fatalf("backfill returned error: %v", err)
	}
	if !done {
		t.Fatalf("expected hitting max_backfill_entries to still report done=true so warming can switch")
	}
	if records.scanCalls != 2 {
		t.Fatalf("expected the scan to stop after the cap (2 pages of size 2), got %d calls", records.scanCalls)
	}
}
