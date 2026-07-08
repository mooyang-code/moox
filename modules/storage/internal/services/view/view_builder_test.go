package view

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestRebuildPendingViewsSkipsFailedViews(t *testing.T) {
	ctx := context.Background()
	meta := newBuilderTestMetadata(
		testView("crypto", "bad", "bad_dataset", "failed", 2, 0),
		testView("crypto", "good", "good_dataset", "pending", 2, 1),
	)
	facts := &builderTestFacts{
		scanErrByDataset: map[string]error{"bad_dataset": errors.New("route missing")},
		scanRows: map[string][]*pb.TimeSeriesRow{
			"good_dataset": {testTimeSeriesRow("crypto", "good_dataset", "BTC-USDT", "1m", "2026-07-07T04:18:00Z")},
		},
	}
	builder := NewBuilder(Options{Metadata: meta, Facts: facts, Views: &builderTestViewWriter{}})

	built, err := builder.RebuildPendingViews(ctx, "crypto")
	if err != nil {
		t.Fatalf("RebuildPendingViews returned error: %v", err)
	}
	if len(built) != 1 || built[0].GetViewId() != "good" {
		t.Fatalf("built views = %v, want only good", viewIDs(built))
	}
	if facts.scanned["bad_dataset"] {
		t.Fatalf("failed view was scanned; failed views should wait for retry_failed schedule")
	}
	if !facts.scanned["good_dataset"] {
		t.Fatalf("good pending view was not rebuilt")
	}
}

func TestRebuildPendingViewsSkipsAlreadyBuildingViews(t *testing.T) {
	ctx := context.Background()
	meta := newBuilderTestMetadata(
		testView("crypto", "bad", "bad_dataset", "building", 2, 1),
		testView("crypto", "good", "good_dataset", "pending", 2, 1),
	)
	facts := &builderTestFacts{
		scanErrByDataset: map[string]error{"bad_dataset": errors.New("route missing")},
		scanRows: map[string][]*pb.TimeSeriesRow{
			"good_dataset": {testTimeSeriesRow("crypto", "good_dataset", "BTC-USDT", "1m", "2026-07-07T04:18:00Z")},
		},
	}
	builder := NewBuilder(Options{Metadata: meta, Facts: facts, Views: &builderTestViewWriter{}})

	built, err := builder.RebuildPendingViews(ctx, "crypto")
	if err != nil {
		t.Fatalf("RebuildPendingViews returned error: %v", err)
	}
	if len(built) != 1 || built[0].GetViewId() != "good" {
		t.Fatalf("built views = %v, want only good", viewIDs(built))
	}
	if facts.scanned["bad_dataset"] {
		t.Fatalf("building view was scanned; already-building views should not be restarted by the regular schedule")
	}
}

func TestRebuildPendingViewsRecoversStaleBuildingViews(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 7, 5, 30, 0, 0, time.UTC)
	stale := testView("crypto", "stale", "stale_dataset", "building", 2, 1)
	stale.BuildStartedAt = now.Add(-11 * time.Minute).Format(time.RFC3339Nano)
	meta := newBuilderTestMetadata(stale)
	facts := &builderTestFacts{
		scanRows: map[string][]*pb.TimeSeriesRow{
			"stale_dataset": {testTimeSeriesRow("crypto", "stale_dataset", "BTC-USDT", "1m", "2026-07-07T05:18:00Z")},
		},
	}
	builder := NewBuilder(Options{Metadata: meta, Facts: facts, Views: &builderTestViewWriter{}, Now: func() time.Time { return now }})

	built, err := builder.RebuildPendingViews(ctx, "crypto")
	if err != nil {
		t.Fatalf("RebuildPendingViews returned error: %v", err)
	}
	if len(built) != 1 || built[0].GetViewId() != "stale" {
		t.Fatalf("built views = %v, want stale", viewIDs(built))
	}
	if !facts.scanned["stale_dataset"] {
		t.Fatalf("stale building view was not rebuilt")
	}
}

func TestRebuildPendingViewsContinuesAfterBuildFailure(t *testing.T) {
	ctx := context.Background()
	meta := newBuilderTestMetadata(
		testView("crypto", "bad", "bad_dataset", "pending", 2, 1),
		testView("crypto", "good", "good_dataset", "pending", 2, 1),
	)
	facts := &builderTestFacts{
		scanErrByDataset: map[string]error{"bad_dataset": errors.New("route missing")},
		scanRows: map[string][]*pb.TimeSeriesRow{
			"good_dataset": {testTimeSeriesRow("crypto", "good_dataset", "BTC-USDT", "1m", "2026-07-07T04:18:00Z")},
		},
	}
	builder := NewBuilder(Options{Metadata: meta, Facts: facts, Views: &builderTestViewWriter{}})

	built, err := builder.RebuildPendingViews(ctx, "crypto")
	if err == nil {
		t.Fatalf("RebuildPendingViews error = nil, want partial build error")
	}
	if len(built) != 1 || built[0].GetViewId() != "good" {
		t.Fatalf("built views = %v, want only good after bad fails", viewIDs(built))
	}
	if !facts.scanned["bad_dataset"] || !facts.scanned["good_dataset"] {
		t.Fatalf("scanned datasets = %v, want both bad and good attempted", facts.scanned)
	}
}

func TestBuildUsesBackfillWindowWhenQueryWindowEmpty(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	meta := newBuilderTestMetadata(testView("crypto", "spot", "kline", "pending", 1, 0))
	facts := &builderTestFacts{
		scanRows: map[string][]*pb.TimeSeriesRow{
			"kline": {testTimeSeriesRow("crypto", "kline", "BTC-USDT", "1m", "2026-07-07T04:18:00Z")},
		},
	}
	builder := NewBuilder(Options{
		Metadata:       meta,
		Facts:          facts,
		Views:          &builderTestViewWriter{},
		BackfillWindow: "30d",
		Now:            func() time.Time { return now },
	})

	if _, err := builder.Build(ctx, "crypto", "spot"); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := facts.scanRanges["kline"].GetStartTime(), "2026-06-08T10:00:00Z"; got != want {
		t.Fatalf("scan start_time = %q, want backfill window %q", got, want)
	}
}

func TestBuildQueryWindowOverridesBackfillWindow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	view := testView("crypto", "spot", "kline", "pending", 1, 0)
	view.QueryWindow = "7d"
	meta := newBuilderTestMetadata(view)
	facts := &builderTestFacts{
		scanRows: map[string][]*pb.TimeSeriesRow{
			"kline": {testTimeSeriesRow("crypto", "kline", "BTC-USDT", "1m", "2026-07-07T04:18:00Z")},
		},
	}
	builder := NewBuilder(Options{
		Metadata:       meta,
		Facts:          facts,
		Views:          &builderTestViewWriter{},
		BackfillWindow: "30d",
		Now:            func() time.Time { return now },
	})

	if _, err := builder.Build(ctx, "crypto", "spot"); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := facts.scanRanges["kline"].GetStartTime(), "2026-07-01T10:00:00Z"; got != want {
		t.Fatalf("scan start_time = %q, want query window %q", got, want)
	}
}

func TestRecordBuildUsesBackfillWindowWhenQueryWindowEmpty(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	meta := newBuilderTestMetadata(testRecordView("crypto", "records_view", "records", "pending", 1, 0))
	facts := &builderTestFacts{
		scanRecordRows: map[string][]*pb.RecordRow{
			"records": {testRecordRow("crypto", "records", "BTC-USDT", "2026-07-07T04:18:00Z")},
		},
	}
	builder := NewBuilder(Options{
		Metadata:       meta,
		Facts:          facts,
		Records:        facts,
		Search:         &builderTestRecordIndexer{},
		BackfillWindow: "30d",
		Now:            func() time.Time { return now },
	})

	if _, err := builder.Build(ctx, "crypto", "records_view"); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := facts.scanRecordRanges["records"].GetStartVersion(), "2026-06-08T10:00:00Z"; got != want {
		t.Fatalf("scan start_version = %q, want backfill window %q", got, want)
	}
}

func TestRecordBuildQueryWindowOverridesBackfillWindow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	view := testRecordView("crypto", "records_view", "records", "pending", 1, 0)
	view.QueryWindow = "7d"
	meta := newBuilderTestMetadata(view)
	facts := &builderTestFacts{
		scanRecordRows: map[string][]*pb.RecordRow{
			"records": {testRecordRow("crypto", "records", "BTC-USDT", "2026-07-07T04:18:00Z")},
		},
	}
	builder := NewBuilder(Options{
		Metadata:       meta,
		Facts:          facts,
		Records:        facts,
		Search:         &builderTestRecordIndexer{},
		BackfillWindow: "30d",
		Now:            func() time.Time { return now },
	})

	if _, err := builder.Build(ctx, "crypto", "records_view"); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := facts.scanRecordRanges["records"].GetStartVersion(), "2026-07-01T10:00:00Z"; got != want {
		t.Fatalf("scan start_version = %q, want query window %q", got, want)
	}
}

type builderTestMetadata struct {
	views []*pb.View
	byID  map[string]*pb.View
}

func newBuilderTestMetadata(views ...*pb.View) *builderTestMetadata {
	meta := &builderTestMetadata{byID: make(map[string]*pb.View)}
	for _, view := range views {
		copied := proto.Clone(view).(*pb.View)
		meta.views = append(meta.views, copied)
		meta.byID[copied.GetViewId()] = copied
	}
	return meta
}

func (m *builderTestMetadata) GetView(_ context.Context, _ string, viewID string) (*pb.View, error) {
	if view := m.byID[viewID]; view != nil {
		return proto.Clone(view).(*pb.View), nil
	}
	return nil, errors.New("view not found")
}

func (m *builderTestMetadata) ListViews(_ context.Context, _ string, _ string, _ string, _ *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	out := make([]*pb.View, 0, len(m.views))
	for _, view := range m.views {
		out = append(out, proto.Clone(view).(*pb.View))
	}
	return out, &pb.PageResult{}, nil
}

func (m *builderTestMetadata) ListViewsByDataset(context.Context, string, string) ([]*pb.View, error) {
	return nil, nil
}

func (m *builderTestMetadata) ListViewColumns(_ context.Context, _ string, viewID string, _ *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	datasetID := "kline"
	if view := m.byID[viewID]; view != nil && view.GetPrimaryDatasetId() != "" {
		datasetID = view.GetPrimaryDatasetId()
	}
	return []*pb.ViewColumn{{
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   datasetID + ".close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}}, &pb.PageResult{}, nil
}

func (m *builderTestMetadata) ListSpaces(context.Context, string, *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	return nil, nil, nil
}

func (m *builderTestMetadata) GetDataset(_ context.Context, _ string, datasetID string) (*pb.Dataset, error) {
	if datasetID == "records" {
		return &pb.Dataset{DataKind: pb.DataKind_DATA_KIND_RECORD}, nil
	}
	return &pb.Dataset{DataKind: pb.DataKind_DATA_KIND_TIME_SERIES}, nil
}

func (m *builderTestMetadata) UpsertView(_ context.Context, item *pb.View) (*pb.View, error) {
	copied := proto.Clone(item).(*pb.View)
	m.byID[copied.GetViewId()] = copied
	for i, view := range m.views {
		if view.GetViewId() == copied.GetViewId() {
			m.views[i] = copied
			return proto.Clone(copied).(*pb.View), nil
		}
	}
	m.views = append(m.views, copied)
	return proto.Clone(copied).(*pb.View), nil
}

func (m *builderTestMetadata) BeginViewBuild(_ context.Context, _ string, viewID string, targetVersion uint64, resultName string) (*pb.View, error) {
	view := proto.Clone(m.byID[viewID]).(*pb.View)
	view.BuildStatus = "building"
	view.BuildingViewVersion = targetVersion
	view.BuildingResult = resultName
	return m.UpsertView(context.Background(), view)
}

func (m *builderTestMetadata) CompleteViewBuild(_ context.Context, _ string, viewID string, targetVersion uint64, resultName string) error {
	view := proto.Clone(m.byID[viewID]).(*pb.View)
	view.BuildStatus = "active"
	view.ActiveViewVersion = targetVersion
	view.ActiveResult = resultName
	view.BuildingViewVersion = 0
	view.BuildingResult = ""
	_, err := m.UpsertView(context.Background(), view)
	return err
}

func (m *builderTestMetadata) FailViewBuild(_ context.Context, _ string, viewID string, _ uint64, _ string, buildErr error) error {
	view := proto.Clone(m.byID[viewID]).(*pb.View)
	view.BuildStatus = "failed"
	view.BuildError = buildErr.Error()
	_, err := m.UpsertView(context.Background(), view)
	return err
}

type builderTestFacts struct {
	scanErrByDataset map[string]error
	scanRows         map[string][]*pb.TimeSeriesRow
	scanRanges       map[string]*pb.TimeRange
	scanRecordRows   map[string][]*pb.RecordRow
	scanRecordRanges map[string]*pb.VersionRange
	scanned          map[string]bool
}

func (f *builderTestFacts) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}

func (f *builderTestFacts) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return &pb.ReadRecordRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}

func (f *builderTestFacts) ScanTimeSeriesRows(_ context.Context, _ string, datasetID string, timeRange *pb.TimeRange, _ []string, _ *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	if f.scanned == nil {
		f.scanned = make(map[string]bool)
	}
	if f.scanRanges == nil {
		f.scanRanges = make(map[string]*pb.TimeRange)
	}
	f.scanned[datasetID] = true
	if timeRange != nil {
		f.scanRanges[datasetID] = proto.Clone(timeRange).(*pb.TimeRange)
	}
	if err := f.scanErrByDataset[datasetID]; err != nil {
		return nil, nil, err
	}
	rows := f.scanRows[datasetID]
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, proto.Clone(row).(*pb.TimeSeriesRow))
	}
	return out, &pb.PageResult{}, nil
}

func (f *builderTestFacts) ScanRecordRows(_ context.Context, _ string, datasetID string, versionRange *pb.VersionRange, _ []string, _ *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	if f.scanned == nil {
		f.scanned = make(map[string]bool)
	}
	if f.scanRecordRanges == nil {
		f.scanRecordRanges = make(map[string]*pb.VersionRange)
	}
	f.scanned[datasetID] = true
	if versionRange != nil {
		f.scanRecordRanges[datasetID] = proto.Clone(versionRange).(*pb.VersionRange)
	}
	if err := f.scanErrByDataset[datasetID]; err != nil {
		return nil, nil, err
	}
	rows := f.scanRecordRows[datasetID]
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, proto.Clone(row).(*pb.RecordRow))
	}
	return out, &pb.PageResult{}, nil
}

type builderTestViewWriter struct{}

func (builderTestViewWriter) CreateResultTable(context.Context, string, []*pb.ViewColumn) error {
	return nil
}

func (builderTestViewWriter) InsertRows(context.Context, string, []*pb.TimeSeriesRow) error {
	return nil
}

type builderTestRecordIndexer struct{}

func (builderTestRecordIndexer) IndexRecordViewRows(context.Context, string, []*pb.ViewColumn, []*pb.RecordRow) error {
	return nil
}

func testView(spaceID string, viewID string, datasetID string, buildStatus string, viewVersion uint64, activeVersion uint64) *pb.View {
	return &pb.View{
		SpaceId:           spaceID,
		ViewId:            viewID,
		PrimaryDatasetId:  datasetID,
		DatasetIds:        []string{datasetID},
		Engine:            "duckdb",
		Status:            "active",
		BuildStatus:       buildStatus,
		ViewVersion:       viewVersion,
		ActiveViewVersion: activeVersion,
	}
}

func testRecordView(spaceID string, viewID string, datasetID string, buildStatus string, viewVersion uint64, activeVersion uint64) *pb.View {
	view := testView(spaceID, viewID, datasetID, buildStatus, viewVersion, activeVersion)
	view.Engine = "bleve"
	return view
}

func testTimeSeriesRow(spaceID string, datasetID string, subjectID string, freq string, dataTime string) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: freq, DataTime: dataTime},
		Columns: []*pb.ColumnValue{{
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.23}},
		}},
	}
}

func testRecordRow(spaceID string, datasetID string, recordID string, version string) *pb.RecordRow {
	return &pb.RecordRow{
		Key: &pb.RecordKey{SpaceId: spaceID, DatasetId: datasetID, RecordId: recordID, Version: version},
		Columns: []*pb.ColumnValue{{
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.23}},
		}},
	}
}

func viewIDs(views []*pb.View) []string {
	out := make([]string, 0, len(views))
	for _, view := range views {
		out = append(out, view.GetViewId())
	}
	return out
}
