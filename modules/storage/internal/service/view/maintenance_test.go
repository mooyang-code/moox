package view

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type maintenanceMetadata struct {
	view         *pb.View
	activated    bool
	claims       int
	claimedIndex string
	activateErr  error
	activateRet  *pb.RetInfo
	failCalls    int
	failErr      error
}

// capacityMaintenanceMetadata records the audit calls made by a real
// maintainer pass while retaining the lightweight metadata fake used by the
// surrounding lifecycle tests.
type capacityMaintenanceMetadata struct {
	maintenanceMetadata
	created *pb.ViewRebuildLog
	updated []*pb.ViewRebuildLog
}

func (m *capacityMaintenanceMetadata) CreateViewRebuildLog(_ context.Context, req *pb.CreateViewRebuildLogReq, _ ...client.Option) (*pb.CreateViewRebuildLogRsp, error) {
	m.created = proto.Clone(req.GetLog()).(*pb.ViewRebuildLog)
	return &pb.CreateViewRebuildLogRsp{RetInfo: successRetInfo(), Log: proto.Clone(m.created).(*pb.ViewRebuildLog)}, nil
}

func (m *capacityMaintenanceMetadata) UpdateViewRebuildLog(_ context.Context, req *pb.UpdateViewRebuildLogReq, _ ...client.Option) (*pb.UpdateViewRebuildLogRsp, error) {
	log := proto.Clone(req.GetLog()).(*pb.ViewRebuildLog)
	m.updated = append(m.updated, log)
	return &pb.UpdateViewRebuildLogRsp{RetInfo: successRetInfo(), Log: log}, nil
}

func (m *capacityMaintenanceMetadata) ListViewRebuildLogs(context.Context, *pb.ListViewRebuildLogsReq, ...client.Option) (*pb.ListViewRebuildLogsRsp, error) {
	return &pb.ListViewRebuildLogsRsp{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: 1, Size: 100}}, nil
}

func (m *capacityMaintenanceMetadata) UpsertSkippedViewRebuildLog(_ context.Context, req *pb.UpsertSkippedViewRebuildLogReq, _ ...client.Option) (*pb.UpsertSkippedViewRebuildLogRsp, error) {
	log := proto.Clone(req.GetLog()).(*pb.ViewRebuildLog)
	m.updated = append(m.updated, log)
	return &pb.UpsertSkippedViewRebuildLogRsp{RetInfo: successRetInfo(), Log: log}, nil
}

func (m *maintenanceMetadata) ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error) {
	return &pb.ListViewsRsp{RetInfo: successRetInfo(), Views: []*pb.View{m.view}, PageResult: &pb.PageResult{Page: 1, Size: 100}}, nil
}
func (m *maintenanceMetadata) GetDataset(_ context.Context, req *pb.GetDatasetReq, _ ...client.Option) (*pb.GetDatasetRsp, error) {
	kind := pb.DataKind_DATA_KIND_RECORD
	if req.GetDatasetId() == "prices" {
		kind = pb.DataKind_DATA_KIND_TIME_SERIES
	}
	return &pb.GetDatasetRsp{RetInfo: successRetInfo(), Dataset: &pb.Dataset{SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), DataKind: kind}}, nil
}
func (m *maintenanceMetadata) ListDatasetSubjects(context.Context, *pb.ListDatasetSubjectsReq, ...client.Option) (*pb.ListDatasetSubjectsRsp, error) {
	return &pb.ListDatasetSubjectsRsp{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: 1, Size: 1000}}, nil
}
func (m *maintenanceMetadata) ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq, ...client.Option) (*pb.ListDatasetColumnsRsp, error) {
	return &pb.ListDatasetColumnsRsp{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: 1, Size: 1000}}, nil
}
func (m *maintenanceMetadata) ClaimViewIndexBuild(_ context.Context, req *pb.ClaimViewIndexBuildReq, _ ...client.Option) (*pb.ClaimViewIndexBuildRsp, error) {
	m.claims++
	m.claimedIndex = req.GetIndexId()
	return &pb.ClaimViewIndexBuildRsp{RetInfo: successRetInfo(), Build: &pb.ViewIndexBuild{BuildId: req.GetBuildId(), State: pb.ViewIndexBuild_PREPARING}}, nil
}
func (m *maintenanceMetadata) UpdateViewIndexBuild(_ context.Context, req *pb.UpdateViewIndexBuildReq, _ ...client.Option) (*pb.UpdateViewIndexBuildRsp, error) {
	return &pb.UpdateViewIndexBuildRsp{RetInfo: successRetInfo(), Build: &pb.ViewIndexBuild{BuildId: req.GetBuildId(), State: req.GetNextState()}}, nil
}
func (m *maintenanceMetadata) ActivateViewIndex(context.Context, *pb.ActivateViewIndexReq, ...client.Option) (*pb.ActivateViewIndexRsp, error) {
	m.activated = true
	if m.activateErr != nil {
		return nil, m.activateErr
	}
	view := proto.Clone(m.view).(*pb.View)
	if m.claimedIndex != "" {
		view.ActiveIndexId = m.claimedIndex
	}
	ret := m.activateRet
	if ret == nil {
		ret = successRetInfo()
	}
	return &pb.ActivateViewIndexRsp{RetInfo: ret, View: view}, nil
}

func (m *maintenanceMetadata) FailViewIndexBuild(context.Context, *pb.FailViewIndexBuildReq, ...client.Option) (*pb.FailViewIndexBuildRsp, error) {
	m.failCalls++
	if m.failErr != nil {
		err := m.failErr
		m.failErr = nil
		return nil, err
	}
	return &pb.FailViewIndexBuildRsp{RetInfo: successRetInfo()}, nil
}

func TestNeedsRebuildTriggers(t *testing.T) {
	base := &pb.View{
		SpaceId: "s", ViewId: "v", ActiveIndexId: "idx",
		DesiredViewRevision: 1, ActiveViewRevision: 1, KeepDuration: "24h",
	}
	if needsRebuild(base, viewindex.ViewIndexStats{Exists: true}) {
		t.Fatal("stable view unexpectedly needs rebuild")
	}
	missing := proto.Clone(base).(*pb.View)
	missing.ActiveIndexId = ""
	if !needsRebuild(missing, viewindex.ViewIndexStats{Exists: true}) {
		t.Fatal("missing active index did not trigger rebuild")
	}
	revision := proto.Clone(base).(*pb.View)
	revision.DesiredViewRevision = 2
	if !needsRebuild(revision, viewindex.ViewIndexStats{Exists: true}) {
		t.Fatal("desired revision did not trigger rebuild")
	}
	wide := viewindex.ViewIndexStats{Exists: true, IndexedFrom: "2026-07-17T00:00:00Z", IndexedTo: "2026-07-20T00:00:00Z"}
	if !needsRebuild(base, wide) {
		t.Fatal("coverage wider than twice keep_duration did not trigger rebuild")
	}
	permanent := proto.Clone(base).(*pb.View)
	permanent.KeepDuration = "0"
	if needsRebuild(permanent, wide) {
		t.Fatal("permanent view triggered time-based rebuild")
	}
	if !needsCapacityMaintenanceRebuild(base, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 512}, MaintenanceOptions{MaxViewFileBytes: 512}) {
		t.Fatal("physical byte watermark did not trigger rebuild")
	}
	if needsCapacityMaintenanceRebuild(permanent, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1 << 40}, MaintenanceOptions{MaxViewFileBytes: 1}) {
		t.Fatal("permanent view triggered an unrecoverable physical rebuild")
	}
}

func TestPeriodCapacityPolicyIgnoresGlobalCoverageSpan(t *testing.T) {
	view := &pb.View{
		ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 1,
		KeepDuration: "24h",
		FilterJson:   `{"freq":"1m"}`,
	}
	wide := viewindex.ViewIndexStats{
		Exists: true, IndexedFrom: "2026-01-01T00:00:00Z", IndexedTo: "2026-01-10T00:00:00Z",
		PhysicalBytes: 1 << 20,
	}
	periodPolicy := MaintenanceOptions{RebuildLookbackPeriods: map[string]uint64{"default": 1000}, MaxViewFileBytes: 1 << 30}
	if needsCapacityMaintenanceRebuild(view, wide, periodPolicy) {
		t.Fatal("period-based View must not rebuild repeatedly because global series timestamps span more than keep_duration")
	}
	if needsCapacityMaintenanceWatermark(view, wide, periodPolicy) {
		t.Fatal("period-based View incorrectly crossed the byte watermark")
	}
}

func TestDurationCapacityPolicyStillHonorsGlobalCoverageSpan(t *testing.T) {
	view := &pb.View{
		ActiveIndexId: "metrics-a", ActiveViewRevision: 1, DesiredViewRevision: 1,
		KeepDuration: "24h",
	}
	wide := viewindex.ViewIndexStats{
		Exists: true, IndexedFrom: "2026-01-01T00:00:00Z", IndexedTo: "2026-01-10T00:00:00Z",
		PhysicalBytes: 1 << 20,
	}
	policy := MaintenanceOptions{RebuildLookbackPeriods: map[string]uint64{"default": 1000}, MaxViewFileBytes: 1 << 30}
	if !needsCapacityMaintenanceRebuild(view, wide, policy) {
		t.Fatal("duration-based View must rebuild when global coverage exceeds retention span")
	}
}

func TestRebuildLookbackUsesViewRetentionPerFrequency(t *testing.T) {
	view := &pb.View{KeepDuration: "6h"}
	if got := rebuildLookbackForView(view, 24*time.Hour); got != 6*time.Hour {
		t.Fatalf("view-specific lookback = %s, want 6h", got)
	}
	if got := rebuildLookbackForView(&pb.View{KeepDuration: "0"}, 24*time.Hour); got != 24*time.Hour {
		t.Fatalf("fallback lookback = %s, want 24h", got)
	}
	if got := rebuildLookbackForView(&pb.View{KeepDuration: "not-a-duration"}, 24*time.Hour); got != 24*time.Hour {
		t.Fatalf("invalid view retention should use fallback, got %s", got)
	}
}

func TestRebuildLookbackPeriodsSelectsFrequencyAndDefault(t *testing.T) {
	configured := map[string]uint64{"1m": 4320, "1h": 2880, "1d": 360, "default": 2000}
	if got := rebuildLookbackPeriodsForView(&pb.View{FilterJson: `{"freq":"1m"}`}, configured); got != 4320 {
		t.Fatalf("1m periods = %d, want 4320", got)
	}
	if got := rebuildLookbackPeriodsForView(&pb.View{FilterJson: `{"freq":"1H"}`}, configured); got != 2880 {
		t.Fatalf("1H periods = %d, want 2880", got)
	}
	if got := rebuildLookbackPeriodsForView(&pb.View{FilterJson: `{"freq":"1d"}`}, configured); got != 360 {
		t.Fatalf("1d periods = %d, want 360", got)
	}
	if got := rebuildLookbackPeriodsForView(&pb.View{FilterJson: `{"freq":"30s"}`}, configured); got != 2000 {
		t.Fatalf("default periods = %d, want 2000", got)
	}
	if got := rebuildLookbackPeriodsForView(&pb.View{FilterJson: `{"foo":"bar"}`}, configured); got != 0 {
		t.Fatalf("missing frequency periods = %d, want 0", got)
	}
	if got := rebuildLookbackPeriodsForView(&pb.View{FilterJson: `{"freq":"1m"}`}, nil); got != defaultRebuildLookbackPeriods {
		t.Fatalf("missing configured periods = %d, want default %d", got, defaultRebuildLookbackPeriods)
	}
	if got := rebuildLookbackPeriodsForView(&pb.View{Engine: "duckdb", FilterJson: `{"foo":"bar"}`}, nil); got != 0 {
		t.Fatalf("frequency-less View periods = %d, want 0", got)
	}
}

func TestNeedsLookbackRepairDetectsShortTimeSeriesCoverage(t *testing.T) {
	view := &pb.View{Engine: "duckdb", ActiveIndexId: "idx", KeepDuration: "24h"}
	short := viewindex.ViewIndexStats{
		Exists:      true,
		IndexedFrom: time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339Nano),
		IndexedTo:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	if !needsLookbackRepair(view, short, time.Hour) {
		t.Fatal("short active coverage did not request Primary-backed repair")
	}
	bleve := proto.Clone(view).(*pb.View)
	bleve.Engine = "bleve"
	if needsLookbackRepair(bleve, short, time.Hour) {
		t.Fatal("record View should not request time-series coverage repair")
	}
	complete := short
	complete.IndexedFrom = time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	if needsLookbackRepair(view, complete, time.Hour) {
		t.Fatal("complete active coverage requested an unnecessary repair")
	}
}

func TestCapacityMaintenanceBuildCooldownPreventsImmediateRepeat(t *testing.T) {
	s := &Service{views: map[viewRef]*viewRuntime{}}
	runtime := &viewRuntime{}
	s.views[viewRef{spaceID: "s", viewID: "v"}] = runtime
	now := time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC)
	s.markCapacityMaintenanceBuild("s", "v", now)
	if s.capacityMaintenanceBuildAllowed("s", "v", now.Add(1*time.Minute)) {
		t.Fatal("size-limit rebuild was allowed before cooldown elapsed")
	}
	if !s.capacityMaintenanceBuildAllowed("s", "v", now.Add(capacityMaintenanceRetryInterval)) {
		t.Fatal("size-limit rebuild remained blocked after cooldown elapsed")
	}
}

func TestCapacityMaintenanceBuildWaitsForConsumerToBecomeIdle(t *testing.T) {
	s := &Service{}
	s.consumerState = func(context.Context) (jetstream.ConsumerState, error) {
		return jetstream.ConsumerState{NumPending: capacityMaintenanceBuildBacklogThreshold, NumAckPending: 1}, nil
	}
	if s.capacityMaintenanceBuildIdle(context.Background()) {
		t.Fatal("size-limit rebuild was allowed while the consumer had backlog")
	}
	s.consumerState = func(context.Context) (jetstream.ConsumerState, error) {
		return jetstream.ConsumerState{}, nil
	}
	if !s.capacityMaintenanceBuildIdle(context.Background()) {
		t.Fatal("size-limit rebuild remained blocked after the consumer became idle")
	}
	s.consumerState = func(context.Context) (jetstream.ConsumerState, error) {
		return jetstream.ConsumerState{NumPending: 1, NumAckPending: 1}, nil
	}
	if !s.capacityMaintenanceBuildIdle(context.Background()) {
		t.Fatal("one poison delivery permanently blocked all size-limit rebuilds")
	}
	s.consumerState = func(context.Context) (jetstream.ConsumerState, error) {
		return jetstream.ConsumerState{}, errors.New("eventbus unavailable")
	}
	if s.capacityMaintenanceBuildIdle(context.Background()) {
		t.Fatal("optional size-limit rebuild failed open when backlog state was unavailable")
	}
}

func TestCapacityMaintenanceBuildRequiresConsecutiveIdleChecks(t *testing.T) {
	s := &Service{}
	s.consumerState = func(context.Context) (jetstream.ConsumerState, error) {
		return jetstream.ConsumerState{}, nil
	}
	ref := viewRef{spaceID: "space", viewID: "metrics"}
	for i := uint32(1); i < defaultRebuildIdleChecks; i++ {
		if s.capacityMaintenanceBuildIdleFor(context.Background(), ref, defaultRebuildMaxPending, defaultRebuildIdleChecks) {
			t.Fatalf("size-limit gate opened after %d idle checks", i)
		}
	}
	if !s.capacityMaintenanceBuildIdleFor(context.Background(), ref, defaultRebuildMaxPending, defaultRebuildIdleChecks) {
		t.Fatal("size-limit gate did not open after consecutive idle checks")
	}
	if s.tryAcquireRebuild() == false || s.tryAcquireRebuild() == true {
		t.Fatal("global rebuild permit did not serialize optional rebuilds")
	}
	s.releaseRebuild()
}

func TestFailedMaintenanceBuildStopsWhenWatermarkIsCleared(t *testing.T) {
	view := &pb.View{
		SpaceId: "s", ViewId: "v", ActiveIndexId: "idx",
		DesiredViewRevision: 1, ActiveViewRevision: 1, KeepDuration: "24h",
	}
	capacityMaintenanceExceeded := needsCapacityMaintenanceRebuild(view, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1 << 20}, MaintenanceOptions{MaxViewFileBytes: 1 << 30})
	if capacityMaintenanceExceeded {
		t.Fatal("watermark unexpectedly exceeded")
	}
	// A failed maintenance build with an unchanged revision is safe to stop;
	// the active index is still serving reads and the next watermark crossing
	// will request a fresh A/B build.
	if view.GetDesiredViewRevision() != view.GetActiveViewRevision() {
		t.Fatal("test view is not revision-stable")
	}
	failed := &pb.ViewIndexBuild{UpdatedAt: "2026-08-12T00:00:00Z"}
	if shouldRetryFailedBuild(view, failed, capacityMaintenanceExceeded, time.Date(2026, 8, 12, 0, 1, 0, 0, time.UTC)) {
		t.Fatal("failed maintenance build kept retrying after watermark cleared")
	}
	view.DesiredViewRevision++
	if !shouldRetryFailedBuild(view, failed, true, time.Date(2026, 8, 12, 0, 1, 0, 0, time.UTC)) {
		t.Fatal("failed revision build did not remain retryable")
	}
}

func TestFailedCapacityMaintenanceBuildWaitsForCooldown(t *testing.T) {
	view := &pb.View{DesiredViewRevision: 1, ActiveViewRevision: 1, KeepDuration: "24h"}
	failed := &pb.ViewIndexBuild{UpdatedAt: "2026-08-12T00:00:00Z"}
	if shouldRetryFailedBuild(view, failed, true, time.Date(2026, 8, 12, 0, 29, 59, 0, time.UTC)) {
		t.Fatal("size-limit rebuild retried before cooldown")
	}
	if !shouldRetryFailedBuild(view, failed, true, time.Date(2026, 8, 12, 0, 30, 0, 0, time.UTC)) {
		t.Fatal("size-limit rebuild did not retry after cooldown")
	}
}

func TestFailedBuildMissingActiveIsNotHeldBySizeCooldown(t *testing.T) {
	view := &pb.View{ActiveIndexId: "idx", DesiredViewRevision: 1, ActiveViewRevision: 1, KeepDuration: "24h"}
	failed := &pb.ViewIndexBuild{UpdatedAt: "2026-08-12T00:00:00Z"}
	stats := viewindex.ViewIndexStats{Exists: false, PhysicalBytes: 1 << 40}
	if !needsCapacityMaintenanceRebuild(view, stats, MaintenanceOptions{MaxViewFileBytes: 1}) {
		t.Fatal("missing active physical index should request repair")
	}
	if !shouldRetryFailedBuildWithCause(view, failed, true, false, time.Date(2026, 8, 12, 0, 1, 0, 0, time.UTC)) {
		t.Fatal("missing active physical index was incorrectly held by size cooldown")
	}
}

func TestMaintainerCreatesAndActivatesInitialView(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "records", Engine: "bleve",
		PrimaryDatasetId:    "records",
		DesiredViewRevision: 1,
		Columns: []*pb.ViewColumn{{
			SpaceId: "space", ViewId: "records", OriginId: "records.title", ColumnName: "records.title",
		}},
	}}
	stop, err := svc.StartViewMaintainer(context.Background(), MaintenanceOptions{Metadata: metadata, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if !metadata.activated {
		t.Fatal("initial view was not activated")
	}
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	list, err := svc.ListViewIndexes(context.Background(), &pb.ListViewIndexesReq{AuthInfo: auth})
	if err != nil || len(list.GetIndexes()) != 1 {
		t.Fatalf("indexes=%v err=%v", list, err)
	}
	indexID := viewindex.InactiveViewIndexID("space", "records", "")
	engine, err := svc.engineFor(indexID)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := engine.Query(context.Background(), indexID, viewindex.QuerySpec{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("initial rows=%v err=%v", rows, err)
	}
}

func TestRestoreActiveViewsAttachesExistingPhysicalIndex(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	// Restore must select the engine from View metadata. A fresh Service has
	// no indexID -> engine mapping yet; that mapping is created by attach.
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 1}}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 1,
	}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("restore active view: %v", err)
	}
	svc.mu.RLock()
	engineName := svc.indexEngine["prices-a"]
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	svc.mu.RUnlock()
	if engineName != "bleve" {
		t.Fatalf("restored index engine=%q, want bleve", engineName)
	}
	if runtime == nil {
		t.Fatal("restore did not create view runtime")
	}
	runtime.mu.Lock()
	active := runtime.active
	runtime.mu.Unlock()
	if active != "prices-a" {
		t.Fatalf("restored active index=%q, want prices-a", active)
	}
}

func TestRestoreActiveViewsFailsWhenMetadataActiveIndexIsMissing(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.engines["bleve"] = &queryEngine{}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices",
		ActiveIndexId: "missing-active", ActiveViewRevision: 1, DesiredViewRevision: 1,
	}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("restore missing active error = %v", err)
	}
}

func TestRestoreActiveViewsRejectsPhysicalContractMismatch(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 1, SchemaHash: "old"}}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 2, ActiveViewSchemaHash: "new",
	}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err == nil || !strings.Contains(err.Error(), "contract mismatch") {
		t.Fatalf("restore mismatch error = %v", err)
	}
}

func TestAttachPendingViewBuildRejectsPhysicalSchemaMismatch(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	column := &pb.ViewColumn{SpaceId: "space", ViewId: "prices", ColumnName: "close", OriginId: "prices.close"}
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 2, SchemaHash: "wrong"}}
	view := &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices",
		DesiredViewRevision: 2, Columns: []*pb.ViewColumn{column},
		IndexBuild: &pb.ViewIndexBuild{IndexId: "prices-b", Engine: "bleve", TargetViewVersion: 2},
	}
	if err := svc.AttachPendingViewBuild(context.Background(), view); err == nil || !strings.Contains(err.Error(), "schema hash mismatch") {
		t.Fatalf("pending schema mismatch error = %v", err)
	}
}

func TestActivateResponseErrorReadsBackCommittedActiveIndex(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	const indexID = "records-b"
	columns := []*pb.ViewColumn{{SpaceId: "space", ViewId: "records", OriginId: "records.title", ColumnName: "records.title"}}
	prepared, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: indexID, Schema: &pb.ViewIndexSchema{SpaceId: "space", ViewId: "records", PrimaryDatasetId: "records", DatasetIds: []string{"records"}, ViewVersion: 2, Engine: "bleve", ViewSchemaHash: "schema-2", Columns: columns}})
	if err != nil || prepared.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare: rsp=%v err=%v", prepared, err)
	}
	metadata := &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "records", PrimaryDatasetId: "records", DatasetIds: []string{"records"}, ActiveIndexId: "records-a", ActiveViewRevision: 1, DesiredViewRevision: 2, Engine: "bleve", ActiveColumns: columns}, activateErr: errors.New("response lost")}
	gotErr := svc.activateViewBuild(ctx, MaintenanceOptions{Metadata: metadata, OwnerID: "storage-view", Grace: time.Hour}, auth, metadata.view, "build-1", indexID, "bleve", 2, "schema-2", columns)
	if gotErr == nil {
		t.Fatal("expected activation retry when readback does not commit")
	}
	// The previous call did not expose an active index because the fake had not
	// committed it. Simulate the Metadata transaction having committed before
	// the response was lost and retry.
	metadata.view.ActiveIndexId = indexID
	if err := svc.activateViewBuild(ctx, MaintenanceOptions{Metadata: metadata, OwnerID: "storage-view", Grace: time.Hour}, auth, metadata.view, "build-1", indexID, "bleve", 2, "schema-2", columns); err != nil {
		t.Fatalf("readback activation: %v", err)
	}
	svc.mu.RLock()
	runtime := svc.views[viewRef{spaceID: "space", viewID: "records"}]
	svc.mu.RUnlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active != indexID || runtime.next != "" {
		t.Fatalf("runtime after readback activation: active=%q next=%q", runtime.active, runtime.next)
	}
}

func TestMaintainerBlocksLegacyInFlightViewWithoutActiveContract(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	// Make the physical active index look healthy so reconciliation reaches
	// AttachActiveView rather than silently starting a replacement build.
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 1}}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 2,
		DatasetIds: []string{"prices", "fundamentals"},
	}}
	_, err = svc.StartViewMaintainer(context.Background(), MaintenanceOptions{Metadata: metadata, Interval: time.Hour})
	if !errors.Is(err, errActiveContractUnavailable) {
		t.Fatalf("StartViewMaintainer error=%v, want active-contract migration error", err)
	}
}

func TestMaintainerUsesDatasetKindForTimeSeriesWithoutGrainKeys(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices", DesiredViewRevision: 1,
		Columns: []*pb.ViewColumn{{SpaceId: "space", ViewId: "prices", OriginId: "prices.close", ColumnName: "prices.close"}},
	}}
	stop, err := svc.StartViewMaintainer(context.Background(), MaintenanceOptions{Metadata: metadata, Interval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	indexID := viewindex.InactiveViewIndexID("space", "prices", "")
	engine, err := svc.engineFor(indexID)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := engine.Query(context.Background(), indexID, viewindex.QuerySpec{Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("time-series rows=%v err=%v", rows, err)
	}
}

func TestMaintenanceStatsActiveIndexBeforeAttachingIt(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	statErr := errors.New("legacy duckdb schema requires cleanup and rebuild")
	engine := &queryEngine{statErr: statErr}
	svc.engines["duckdb"] = engine
	view := &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "duckdb", PrimaryDatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 1,
	}
	err = svc.maintainView(context.Background(), MaintenanceOptions{}, svc.internalAuth(), view)
	if !errors.Is(err, statErr) {
		t.Fatalf("maintenance error=%v want=%v", err, statErr)
	}
	svc.mu.RLock()
	runtime := svc.views[viewRef{spaceID: "space", viewID: "prices"}]
	svc.mu.RUnlock()
	if runtime != nil {
		runtime.mu.Lock()
		active := runtime.active
		runtime.mu.Unlock()
		if active != "" {
			t.Fatalf("invalid active index attached before schema validation: %q", active)
		}
	}
	if _, exists := svc.indexEngine["prices-a"]; exists {
		t.Fatal("invalid active index mapping remains attached")
	}
}

func TestMaintenanceRebuildsMissingPhysicalActiveIndex(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	engine := &queryEngine{stats: viewindex.ViewIndexStats{Exists: false}}
	svc.engines["duckdb"] = engine
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "duckdb", PrimaryDatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 1,
	}}
	err = svc.maintainView(context.Background(), MaintenanceOptions{
		Metadata: metadata, OwnerID: "owner",
	}, svc.internalAuth(), metadata.view)
	if err == nil || !strings.Contains(err.Error(), "cannot be rebuilt without a range reader") {
		t.Fatalf("missing active should fail closed, err=%v", err)
	}
	if metadata.claims != 1 || metadata.activated {
		t.Fatalf("missing active unexpectedly claimed/activated replacement: claims=%d activated=%v", metadata.claims, metadata.activated)
	}
}
