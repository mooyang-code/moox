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

type reconcileMetadata struct {
	view         *pb.View
	activated    bool
	claims       int
	claimedIndex string
	activateErr  error
	activateRet  *pb.RetInfo
	failCalls    int
	failErr      error
}

func (m *reconcileMetadata) ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error) {
	return &pb.ListViewsRsp{RetInfo: successRetInfo(), Views: []*pb.View{m.view}, PageResult: &pb.PageResult{Page: 1, Size: 100}}, nil
}
func (m *reconcileMetadata) GetDataset(_ context.Context, req *pb.GetDatasetReq, _ ...client.Option) (*pb.GetDatasetRsp, error) {
	kind := pb.DataKind_DATA_KIND_RECORD
	if req.GetDatasetId() == "prices" {
		kind = pb.DataKind_DATA_KIND_TIME_SERIES
	}
	return &pb.GetDatasetRsp{RetInfo: successRetInfo(), Dataset: &pb.Dataset{SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), DataKind: kind}}, nil
}
func (m *reconcileMetadata) ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq, ...client.Option) (*pb.ListDatasetColumnsRsp, error) {
	return &pb.ListDatasetColumnsRsp{RetInfo: successRetInfo(), PageResult: &pb.PageResult{Page: 1, Size: 1000}}, nil
}
func (m *reconcileMetadata) ClaimViewIndexBuild(_ context.Context, req *pb.ClaimViewIndexBuildReq, _ ...client.Option) (*pb.ClaimViewIndexBuildRsp, error) {
	m.claims++
	m.claimedIndex = req.GetIndexId()
	return &pb.ClaimViewIndexBuildRsp{RetInfo: successRetInfo(), Build: &pb.ViewIndexBuild{BuildId: req.GetBuildId(), State: pb.ViewIndexBuild_PREPARING}}, nil
}
func (m *reconcileMetadata) UpdateViewIndexBuild(_ context.Context, req *pb.UpdateViewIndexBuildReq, _ ...client.Option) (*pb.UpdateViewIndexBuildRsp, error) {
	return &pb.UpdateViewIndexBuildRsp{RetInfo: successRetInfo(), Build: &pb.ViewIndexBuild{BuildId: req.GetBuildId(), State: req.GetNextState()}}, nil
}
func (m *reconcileMetadata) ActivateViewIndex(context.Context, *pb.ActivateViewIndexReq, ...client.Option) (*pb.ActivateViewIndexRsp, error) {
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

func (m *reconcileMetadata) FailViewIndexBuild(context.Context, *pb.FailViewIndexBuildReq, ...client.Option) (*pb.FailViewIndexBuildRsp, error) {
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
	if !needsSizeLimitRebuild(base, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 512}, ReconcilerOptions{MaxViewFileBytes: 512}) {
		t.Fatal("physical byte watermark did not trigger rebuild")
	}
	if needsSizeLimitRebuild(permanent, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1 << 40}, ReconcilerOptions{MaxViewFileBytes: 1}) {
		t.Fatal("permanent view triggered an unrecoverable physical rebuild")
	}
}

func TestSizeLimitBuildCooldownPreventsImmediateRepeat(t *testing.T) {
	s := &Service{views: map[viewRef]*viewRuntime{}}
	runtime := &viewRuntime{}
	s.views[viewRef{spaceID: "s", viewID: "v"}] = runtime
	now := time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC)
	s.markSizeLimitBuild("s", "v", now)
	if s.sizeLimitBuildAllowed("s", "v", now.Add(1*time.Minute)) {
		t.Fatal("size-limit rebuild was allowed before cooldown elapsed")
	}
	if !s.sizeLimitBuildAllowed("s", "v", now.Add(sizeLimitRebuildRetryInterval)) {
		t.Fatal("size-limit rebuild remained blocked after cooldown elapsed")
	}
}

func TestSizeLimitBuildWaitsForConsumerToBecomeIdle(t *testing.T) {
	s := &Service{}
	s.consumerState = func(context.Context) (jetstream.ConsumerState, error) {
		return jetstream.ConsumerState{NumPending: sizeLimitBuildBacklogThreshold, NumAckPending: 1}, nil
	}
	if s.sizeLimitBuildIdle(context.Background()) {
		t.Fatal("size-limit rebuild was allowed while the consumer had backlog")
	}
	s.consumerState = func(context.Context) (jetstream.ConsumerState, error) {
		return jetstream.ConsumerState{}, nil
	}
	if !s.sizeLimitBuildIdle(context.Background()) {
		t.Fatal("size-limit rebuild remained blocked after the consumer became idle")
	}
	s.consumerState = func(context.Context) (jetstream.ConsumerState, error) {
		return jetstream.ConsumerState{NumPending: 1, NumAckPending: 1}, nil
	}
	if !s.sizeLimitBuildIdle(context.Background()) {
		t.Fatal("one poison delivery permanently blocked all size-limit rebuilds")
	}
	s.consumerState = func(context.Context) (jetstream.ConsumerState, error) {
		return jetstream.ConsumerState{}, errors.New("eventbus unavailable")
	}
	if s.sizeLimitBuildIdle(context.Background()) {
		t.Fatal("optional size-limit rebuild failed open when backlog state was unavailable")
	}
}

func TestSizeLimitBuildRequiresConsecutiveIdleChecks(t *testing.T) {
	s := &Service{}
	s.consumerState = func(context.Context) (jetstream.ConsumerState, error) {
		return jetstream.ConsumerState{}, nil
	}
	ref := viewRef{spaceID: "space", viewID: "metrics"}
	for i := uint32(1); i < defaultRebuildIdleChecks; i++ {
		if s.sizeLimitBuildIdleFor(context.Background(), ref, defaultRebuildMaxPending, defaultRebuildIdleChecks) {
			t.Fatalf("size-limit gate opened after %d idle checks", i)
		}
	}
	if !s.sizeLimitBuildIdleFor(context.Background(), ref, defaultRebuildMaxPending, defaultRebuildIdleChecks) {
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
	sizeLimitExceeded := needsSizeLimitRebuild(view, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1 << 20}, ReconcilerOptions{MaxViewFileBytes: 1 << 30})
	if sizeLimitExceeded {
		t.Fatal("watermark unexpectedly exceeded")
	}
	// A failed maintenance build with an unchanged revision is safe to stop;
	// the active index is still serving reads and the next watermark crossing
	// will request a fresh A/B build.
	if view.GetDesiredViewRevision() != view.GetActiveViewRevision() {
		t.Fatal("test view is not revision-stable")
	}
	failed := &pb.ViewIndexBuild{UpdatedAt: "2026-08-12T00:00:00Z"}
	if shouldRetryFailedBuild(view, failed, sizeLimitExceeded, time.Date(2026, 8, 12, 0, 1, 0, 0, time.UTC)) {
		t.Fatal("failed maintenance build kept retrying after watermark cleared")
	}
	view.DesiredViewRevision++
	if !shouldRetryFailedBuild(view, failed, true, time.Date(2026, 8, 12, 0, 1, 0, 0, time.UTC)) {
		t.Fatal("failed revision build did not remain retryable")
	}
}

func TestFailedSizeLimitBuildWaitsForCooldown(t *testing.T) {
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
	if !needsSizeLimitRebuild(view, stats, ReconcilerOptions{MaxViewFileBytes: 1}) {
		t.Fatal("missing active physical index should request repair")
	}
	if !shouldRetryFailedBuildWithCause(view, failed, true, false, time.Date(2026, 8, 12, 0, 1, 0, 0, time.UTC)) {
		t.Fatal("missing active physical index was incorrectly held by size cooldown")
	}
}

func TestReconcilerCreatesAndActivatesInitialView(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	metadata := &reconcileMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "records", Engine: "bleve",
		PrimaryDatasetId:    "records",
		DesiredViewRevision: 1,
		Columns: []*pb.ViewColumn{{
			SpaceId: "space", ViewId: "records", OriginId: "records.title", ColumnName: "records.title",
		}},
	}}
	stop, err := svc.StartReconciler(context.Background(), ReconcilerOptions{Metadata: metadata, Interval: time.Hour})
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
	metadata := &reconcileMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 1,
	}}
	if err := svc.RestoreActiveViews(context.Background(), ReconcilerOptions{Metadata: metadata}); err != nil {
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
	metadata := &reconcileMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices",
		ActiveIndexId: "missing-active", ActiveViewRevision: 1, DesiredViewRevision: 1,
	}}
	if err := svc.RestoreActiveViews(context.Background(), ReconcilerOptions{Metadata: metadata}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("restore missing active error = %v", err)
	}
}

func TestRestoreActiveViewsRejectsPhysicalContractMismatch(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 1, SchemaHash: "old"}}
	metadata := &reconcileMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 2, ActiveViewSchemaHash: "new",
	}}
	if err := svc.RestoreActiveViews(context.Background(), ReconcilerOptions{Metadata: metadata}); err == nil || !strings.Contains(err.Error(), "contract mismatch") {
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
	metadata := &reconcileMetadata{view: &pb.View{SpaceId: "space", ViewId: "records", PrimaryDatasetId: "records", DatasetIds: []string{"records"}, ActiveIndexId: "records-a", ActiveViewRevision: 1, DesiredViewRevision: 2, Engine: "bleve", ActiveColumns: columns}, activateErr: errors.New("response lost")}
	gotErr := svc.activateViewBuild(ctx, ReconcilerOptions{Metadata: metadata, OwnerID: "storage-view", Grace: time.Hour}, auth, metadata.view, "build-1", indexID, "bleve", 2, "schema-2", columns)
	if gotErr == nil {
		t.Fatal("expected activation retry when readback does not commit")
	}
	// The previous call did not expose an active index because the fake had not
	// committed it. Simulate the Metadata transaction having committed before
	// the response was lost and retry.
	metadata.view.ActiveIndexId = indexID
	if err := svc.activateViewBuild(ctx, ReconcilerOptions{Metadata: metadata, OwnerID: "storage-view", Grace: time.Hour}, auth, metadata.view, "build-1", indexID, "bleve", 2, "schema-2", columns); err != nil {
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

func TestReconcilerBlocksLegacyInFlightViewWithoutActiveContract(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	// Make the physical active index look healthy so reconciliation reaches
	// AttachActiveView rather than silently starting a replacement build.
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 1}}
	metadata := &reconcileMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 2,
		DatasetIds: []string{"prices", "fundamentals"},
	}}
	_, err = svc.StartReconciler(context.Background(), ReconcilerOptions{Metadata: metadata, Interval: time.Hour})
	if !errors.Is(err, errActiveContractUnavailable) {
		t.Fatalf("StartReconciler error=%v, want active-contract migration error", err)
	}
}

func TestReconcilerUsesDatasetKindForTimeSeriesWithoutGrainKeys(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	metadata := &reconcileMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", PrimaryDatasetId: "prices", DesiredViewRevision: 1,
		Columns: []*pb.ViewColumn{{SpaceId: "space", ViewId: "prices", OriginId: "prices.close", ColumnName: "prices.close"}},
	}}
	stop, err := svc.StartReconciler(context.Background(), ReconcilerOptions{Metadata: metadata, Interval: time.Hour})
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

func TestReconcileStatsActiveIndexBeforeAttachingIt(t *testing.T) {
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
	err = svc.reconcileView(context.Background(), ReconcilerOptions{}, svc.internalAuth(), view)
	if !errors.Is(err, statErr) {
		t.Fatalf("reconcile error=%v want=%v", err, statErr)
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

func TestReconcileRebuildsMissingPhysicalActiveIndex(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	engine := &queryEngine{stats: viewindex.ViewIndexStats{Exists: false}}
	svc.engines["duckdb"] = engine
	metadata := &reconcileMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "duckdb", PrimaryDatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 1,
	}}
	err = svc.reconcileView(context.Background(), ReconcilerOptions{
		Metadata: metadata, OwnerID: "owner",
	}, svc.internalAuth(), metadata.view)
	if err == nil || !strings.Contains(err.Error(), "cannot be rebuilt without a range reader") {
		t.Fatalf("missing active should fail closed, err=%v", err)
	}
	if metadata.claims != 1 || metadata.activated {
		t.Fatalf("missing active unexpectedly claimed/activated replacement: claims=%d activated=%v", metadata.claims, metadata.activated)
	}
}
