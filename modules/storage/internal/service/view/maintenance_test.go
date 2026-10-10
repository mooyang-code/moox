package view

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type maintenanceMetadata struct {
	view         *pb.View
	activated    bool
	claims       int
	extensions   int
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
func (m *maintenanceMetadata) ListDatasetSubjects(_ context.Context, req *pb.ListDatasetSubjectsReq, _ ...client.Option) (*pb.ListDatasetSubjectsRsp, error) {
	var items []*pb.DatasetSubject
	if req.GetDatasetId() == "prices" {
		items = []*pb.DatasetSubject{
			{SpaceId: req.GetSpaceId(), DatasetId: "prices", SubjectId: "A"},
			{SpaceId: req.GetSpaceId(), DatasetId: "prices", SubjectId: "B"},
		}
	}
	return &pb.ListDatasetSubjectsRsp{RetInfo: successRetInfo(), DatasetSubjects: items, PageResult: &pb.PageResult{Page: 1, Size: 1000}}, nil
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

func (m *maintenanceMetadata) CommitViewSchemaExtension(_ context.Context, req *pb.CommitViewSchemaExtensionReq, _ ...client.Option) (*pb.CommitViewSchemaExtensionRsp, error) {
	m.extensions++
	view := proto.Clone(m.view).(*pb.View)
	view.ActiveViewRevision = req.GetExpectedDesiredRevision()
	view.ActiveViewSchemaHash = req.GetViewSchemaHash()
	view.ActiveColumns = nil
	for _, column := range req.GetColumns() {
		if column != nil {
			view.ActiveColumns = append(view.ActiveColumns, proto.Clone(column).(*pb.ViewColumn))
		}
	}
	m.view = view
	return &pb.CommitViewSchemaExtensionRsp{RetInfo: successRetInfo(), View: proto.Clone(view).(*pb.View)}, nil
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
		SpaceId: "s", ViewId: "v", ActiveIndexId: "idx", Freq: "1m",
		DesiredViewRevision: 1, ActiveViewRevision: 1,
	}
	if needsActiveOrRevisionRebuild(base, viewindex.ViewIndexStats{Exists: true}) {
		t.Fatal("stable view unexpectedly needs rebuild")
	}
	missing := proto.Clone(base).(*pb.View)
	missing.ActiveIndexId = ""
	if !needsActiveOrRevisionRebuild(missing, viewindex.ViewIndexStats{Exists: true}) {
		t.Fatal("missing active index did not trigger rebuild")
	}
	revision := proto.Clone(base).(*pb.View)
	revision.DesiredViewRevision = 2
	if !needsActiveOrRevisionRebuild(revision, viewindex.ViewIndexStats{Exists: true}) {
		t.Fatal("desired revision did not trigger rebuild")
	}
	wide := viewindex.ViewIndexStats{Exists: true, IndexedFrom: "2026-01-01T00:00:00Z", IndexedTo: "2026-07-20T00:00:00Z"}
	if needsCapacityMaintenanceRebuild(base, wide, MaintenanceOptions{MaxViewFileBytes: 1 << 30}) {
		t.Fatal("a wide time span alone triggered a rebuild; Views keep bars, not a time window")
	}
	if !needsCapacityMaintenanceRebuild(base, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 512}, MaintenanceOptions{MaxViewFileBytes: 512}) {
		t.Fatal("physical byte watermark did not trigger rebuild")
	}
	record := proto.Clone(base).(*pb.View)
	record.Freq, record.Engine = "", "bleve"
	if needsCapacityMaintenanceRebuild(record, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1 << 40}, MaintenanceOptions{MaxViewFileBytes: 1}) {
		t.Fatal("a record View triggered an unrecoverable physical rebuild")
	}
	if !permanentViewFileCapacityExceeded(record, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1024}, MaintenanceOptions{MaxViewFileBytes: 512}) {
		t.Fatal("record view over the file limit was not classified for alerting")
	}
	if permanentViewFileCapacityExceeded(base, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1024}, MaintenanceOptions{MaxViewFileBytes: 512}) {
		t.Fatal("bar-bounded view was incorrectly classified as permanent")
	}
}

func TestRevisionRepairIsNotClassifiedAsOptionalCapacityMaintenance(t *testing.T) {
	view := &pb.View{
		SpaceId: "crypto", ViewId: "view_binance_kline_1m", ActiveIndexId: "active", Freq: "1m",
		DesiredViewRevision: 2, ActiveViewRevision: 1,
	}
	stats := viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 512}
	if !needsActiveOrRevisionRebuild(view, stats) {
		t.Fatal("revision change did not require repair")
	}
	if isCapacityMaintenanceOnly(view, stats, MaintenanceOptions{MaxViewFileBytes: 512}, nil, false, true) {
		t.Fatal("revision repair was classified as optional capacity maintenance")
	}
}

func TestViewBarsComeFromThePolicyForTimeSeriesViews(t *testing.T) {
	if got := viewBars(&pb.View{Freq: "1m"}, MaintenanceOptions{Bars: 4320}); got != 4320 {
		t.Fatalf("time-series View bars = %d, want the policy 4320", got)
	}
	if got := viewBars(&pb.View{Freq: "1h"}, MaintenanceOptions{}); got != defaultViewBars {
		t.Fatalf("time-series View bars without a policy = %d, want %d", got, defaultViewBars)
	}
	if got := viewBars(&pb.View{Engine: "bleve"}, MaintenanceOptions{Bars: 4320}); got != 0 {
		t.Fatalf("record View bars = %d, want 0 (keeps every row)", got)
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

func TestSeriesCapacityChecksUsePerIndexJitterAndHourlyRetry(t *testing.T) {
	svc := &Service{}
	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	offsets := []time.Duration{40 * time.Minute, 10 * time.Minute}
	sourceCalls := 0
	jitterSource := func(maximum time.Duration) time.Duration {
		if maximum != time.Hour {
			t.Fatalf("jitter maximum = %s, want 1h", maximum)
		}
		value := offsets[sourceCalls]
		sourceCalls++
		return value
	}
	first := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "view-kline"}, indexID: "index-a"}
	second := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "view-kline-2"}, indexID: "index-b"}
	interval, jitter, err := normalizeCapacityCheckSchedule(MaintenanceOptions{
		CapacityCheckInterval: time.Hour,
		CapacityCheckJitter:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run, _ := svc.beginSeriesCapacityCheck(first, start, interval, jitter, jitterSource); run {
		t.Fatal("first index scanned before its jitter phase")
	}
	if run, _ := svc.beginSeriesCapacityCheck(second, start, interval, jitter, jitterSource); run {
		t.Fatal("second index scanned before its jitter phase")
	}
	if sourceCalls != 2 {
		t.Fatalf("jitter source called %d times, want once per index", sourceCalls)
	}

	if run, _ := svc.beginSeriesCapacityCheck(second, start.Add(10*time.Minute), interval, jitter, jitterSource); !run {
		t.Fatal("second index did not scan at its own randomized phase")
	}
	if run, _ := svc.beginSeriesCapacityCheck(first, start.Add(40*time.Minute), interval, jitter, jitterSource); run {
		t.Fatal("another View started a second outstanding capacity scan")
	}
	svc.finishSeriesCapacityCheck(second, viewindex.SeriesCapacityResult{}, errors.New("temporary scan failure"))
	if run, _ := svc.beginSeriesCapacityCheck(second, start.Add(11*time.Minute), interval, jitter, jitterSource); run {
		t.Fatal("failed capacity scan was retried by the one-minute maintenance loop")
	}
	if run, _ := svc.beginSeriesCapacityCheck(first, start.Add(40*time.Minute), interval, jitter, jitterSource); !run {
		t.Fatal("first index did not scan at its own randomized phase")
	}
	if run, _ := svc.beginSeriesCapacityCheck(first, start.Add(41*time.Minute), interval, jitter, jitterSource); run {
		t.Fatal("concurrent or immediate duplicate capacity scan was allowed")
	}
	offender := viewindex.SeriesCapacityResult{Exceeded: true, SubjectID: "subject-a"}
	svc.finishSeriesCapacityCheck(first, offender, nil)
	if run, cached := svc.beginSeriesCapacityCheck(first, start.Add(42*time.Minute), interval, jitter, jitterSource); run || !cached.Exceeded {
		t.Fatalf("hourly cached result = run %v, offender %#v; want cached capacity breach", run, cached)
	}
	if run, _ := svc.beginSeriesCapacityCheck(second, start.Add(70*time.Minute), interval, jitter, jitterSource); !run {
		t.Fatal("failed capacity scan was not retried at the next hourly slot")
	}
}

func TestPermanentViewCapacityOverflowIsReportedOnceUntilRecovered(t *testing.T) {
	svc := &Service{}
	ref := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "permanent"}, indexID: "idx-a"}
	if newlyExceeded, count := svc.updatePermanentCapacityOverLimit(ref, true); !newlyExceeded || count != 1 {
		t.Fatalf("first overflow transition = (%v, %d), want (true, 1)", newlyExceeded, count)
	}
	if newlyExceeded, count := svc.updatePermanentCapacityOverLimit(ref, true); newlyExceeded || count != 1 {
		t.Fatalf("persistent overflow transition = (%v, %d), want (false, 1)", newlyExceeded, count)
	}
	if newlyExceeded, count := svc.updatePermanentCapacityOverLimit(ref, false); newlyExceeded || count != 0 {
		t.Fatalf("recovered overflow transition = (%v, %d), want (false, 0)", newlyExceeded, count)
	}
	if newlyExceeded, count := svc.updatePermanentCapacityOverLimit(ref, true); !newlyExceeded || count != 1 {
		t.Fatalf("new overflow after recovery = (%v, %d), want (true, 1)", newlyExceeded, count)
	}
	svc.reconcileCapacityCheckInventory(map[capacityCheckRef]struct{}{})
	if got := len(svc.permanentCapacityOverLimit); got != 0 {
		t.Fatalf("inactive overflow state count = %d, want 0", got)
	}
}

func TestPermanentViewFileCapacityObserverPublishesAlertGauge(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := observability.NewViewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{metrics: metrics}
	view := &pb.View{SpaceId: "crypto", ViewId: "permanent", ActiveIndexId: "idx"}
	opts := MaintenanceOptions{MaxViewFileBytes: 512}
	svc.observePermanentViewCapacityLimit(view, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1024}, opts)
	if got := gatheredGaugeValue(t, registry, "moox_storage_view_permanent_view_capacity_over_limit"); got != 1 {
		t.Fatalf("permanent capacity alert gauge = %f, want 1", got)
	}
	svc.observePermanentViewCapacityLimit(view, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 1024}, opts)
	if got := gatheredGaugeValue(t, registry, "moox_storage_view_permanent_view_capacity_over_limit"); got != 1 {
		t.Fatalf("persistent permanent capacity alert gauge = %f, want 1", got)
	}
	svc.observePermanentViewCapacityLimit(view, viewindex.ViewIndexStats{Exists: true, PhysicalBytes: 511}, opts)
	if got := gatheredGaugeValue(t, registry, "moox_storage_view_permanent_view_capacity_over_limit"); got != 0 {
		t.Fatalf("recovered permanent capacity alert gauge = %f, want 0", got)
	}
}

func TestPermanentViewFileCapacityDoesNotClaimRebuild(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	engine := &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 1, PhysicalBytes: 2}}
	svc.engines["duckdb"] = engine
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "permanent", DatasetId: "metrics", Engine: "duckdb",
		ActiveIndexId: "permanent-a", ActiveViewRevision: 1, DesiredViewRevision: 1, Status: "active",
	}}
	opts := MaintenanceOptions{Metadata: metadata, OwnerID: "owner", MaxViewFileBytes: 1}
	for range 2 {
		if err := svc.maintainView(context.Background(), opts, svc.internalAuth(), metadata.view); err != nil {
			t.Fatalf("maintainView error = %v", err)
		}
	}
	if metadata.claims != 0 {
		t.Fatalf("permanent View over the file limit claimed %d rebuilds, want none", metadata.claims)
	}
	if len(svc.permanentCapacityOverLimit) != 1 {
		t.Fatalf("permanent View alert state count = %d, want one active alert", len(svc.permanentCapacityOverLimit))
	}
}

func gatheredGaugeValue(t *testing.T, registry *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == name && len(family.GetMetric()) > 0 {
			return family.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("gauge metric %q was not registered", name)
	return 0
}

func TestSeriesCapacityChecksResampleInitialJitterOnActiveIndexSwitch(t *testing.T) {
	svc := &Service{}
	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	offsets := []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute}
	sourceCalls := 0
	jitterSource := func(maximum time.Duration) time.Duration {
		if maximum != time.Hour {
			t.Fatalf("jitter maximum = %s, want 1h", maximum)
		}
		if sourceCalls >= len(offsets) {
			t.Fatalf("jitter source called more than %d times", len(offsets))
		}
		value := offsets[sourceCalls]
		sourceCalls++
		return value
	}
	interval, jitter, err := normalizeCapacityCheckSchedule(MaintenanceOptions{
		CapacityCheckInterval: time.Hour,
		CapacityCheckJitter:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	view := viewRef{spaceID: "crypto", viewID: "view-kline"}
	indexA := capacityCheckRef{viewRef: view, indexID: "index-a"}
	indexB := capacityCheckRef{viewRef: view, indexID: "index-b"}

	if run, _ := svc.beginSeriesCapacityCheck(indexA, start, interval, jitter, jitterSource); run {
		t.Fatal("initial active index scanned before its randomized phase")
	}
	if run, _ := svc.beginSeriesCapacityCheck(indexA, start.Add(10*time.Minute), interval, jitter, jitterSource); !run {
		t.Fatal("initial active index did not scan at its randomized phase")
	}
	svc.finishSeriesCapacityCheck(indexA, viewindex.SeriesCapacityResult{}, nil)

	if run, _ := svc.beginSeriesCapacityCheck(indexB, start.Add(20*time.Minute), interval, jitter, jitterSource); run {
		t.Fatal("switched active index scanned before its new randomized phase")
	}
	if sourceCalls != 2 {
		t.Fatalf("jitter source calls after switching to index B = %d, want 2", sourceCalls)
	}
	if run, _ := svc.beginSeriesCapacityCheck(indexB, start.Add(40*time.Minute), interval, jitter, jitterSource); !run {
		t.Fatal("switched active index did not scan at its randomized phase")
	}
	svc.finishSeriesCapacityCheck(indexB, viewindex.SeriesCapacityResult{}, nil)

	if run, _ := svc.beginSeriesCapacityCheck(indexA, start.Add(50*time.Minute), interval, jitter, jitterSource); run {
		t.Fatal("index A reused its stale phase after switching away and back")
	}
	if sourceCalls != 3 {
		t.Fatalf("jitter source calls after switching back to index A = %d, want 3", sourceCalls)
	}
	if run, _ := svc.beginSeriesCapacityCheck(indexA, start.Add(80*time.Minute), interval, jitter, jitterSource); !run {
		t.Fatal("index A did not scan at its newly sampled phase after switching back")
	}
}

func TestSeriesCapacityScanQueueDelayIsObservedForSerialViews(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := observability.NewViewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{metrics: metrics}
	now := time.Now().UTC().Add(-10 * time.Minute)
	interval, jitter, err := normalizeCapacityCheckSchedule(MaintenanceOptions{CapacityCheckInterval: time.Hour, CapacityCheckJitter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	first := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "first"}, indexID: "idx-first"}
	second := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "second"}, indexID: "idx-second"}
	zeroJitter := func(time.Duration) time.Duration { return 0 }
	if run, _ := svc.beginSeriesCapacityCheck(first, now, interval, jitter, zeroJitter); !run {
		t.Fatal("first due capacity scan did not start")
	}
	if run, _ := svc.beginSeriesCapacityCheck(second, now, interval, jitter, zeroJitter); run {
		t.Fatal("second scan overlapped the first serial capacity scan")
	}
	if run, _ := svc.beginSeriesCapacityCheck(second, now.Add(5*time.Minute), interval, jitter, zeroJitter); run {
		t.Fatal("queued second scan overlapped the first serial capacity scan")
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var overdueAge float64
	for _, family := range families {
		if family.GetName() == "moox_storage_view_capacity_scan_oldest_overdue_seconds" {
			overdueAge = family.GetMetric()[0].GetGauge().GetValue()
		}
	}
	if overdueAge < 300 {
		t.Fatalf("oldest overdue scan age = %f, want at least 300 seconds", overdueAge)
	}
	svc.finishSeriesCapacityCheck(first, viewindex.SeriesCapacityResult{}, nil)
	if run, _ := svc.beginSeriesCapacityCheck(second, time.Now().UTC(), interval, jitter, zeroJitter); !run {
		t.Fatal("queued capacity scan did not start after the preceding scan completed")
	}
	families, err = registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sampleCount uint64
	var sampleSum float64
	for _, family := range families {
		if family.GetName() == "moox_storage_view_capacity_scan_queue_delay_seconds" {
			sampleCount = family.GetMetric()[0].GetHistogram().GetSampleCount()
			sampleSum = family.GetMetric()[0].GetHistogram().GetSampleSum()
		}
	}
	if sampleCount != 2 || sampleSum < 600 {
		t.Fatalf("capacity queue delay samples count=%d sum=%f; want two observations and at least 600 seconds", sampleCount, sampleSum)
	}
}

type inactivePrepareFailureEngine struct {
	*queryEngine
	failingIndex string
	prepared     []string
	queried      []string
	indexRows    map[string][]*pb.RowFieldValues
	removed      map[string]bool
}

func (*inactivePrepareFailureEngine) Engine() string { return "duckdb" }

func (e *inactivePrepareFailureEngine) Prepare(_ context.Context, indexID string, _ viewindex.ViewIndexSchema) error {
	e.prepared = append(e.prepared, indexID)
	if indexID == e.failingIndex {
		return errors.New("injected inactive index prepare failure")
	}
	if e.indexRows == nil {
		e.indexRows = make(map[string][]*pb.RowFieldValues)
	}
	if e.removed == nil {
		e.removed = make(map[string]bool)
	}
	e.removed[indexID] = false
	return nil
}

func (e *inactivePrepareFailureEngine) Query(ctx context.Context, indexID string, spec viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	e.queried = append(e.queried, indexID)
	if e.removed[indexID] {
		return nil, 0, fmt.Errorf("index %s was removed", indexID)
	}
	rows, ok := e.indexRows[indexID]
	if !ok {
		return nil, 0, fmt.Errorf("index %s does not exist", indexID)
	}
	e.queryEngine.calls++
	e.queryEngine.spec = spec
	return rows, int64(len(rows)), nil
}

func (e *inactivePrepareFailureEngine) Remove(_ context.Context, indexID string) error {
	if e.removed == nil {
		e.removed = make(map[string]bool)
	}
	e.removed[indexID] = true
	delete(e.indexRows, indexID)
	return nil
}

func TestInactiveCapacityBuildFailureLeavesActiveSlotReadable(t *testing.T) {
	ctx := context.Background()
	const (
		spaceID  = "space"
		viewID   = "prices"
		activeID = "prices-a"
	)
	columns := []*pb.ViewColumn{{SpaceId: spaceID, ViewId: viewID, OriginId: "close", ColumnName: "close"}}
	inactiveID := viewindex.InactiveViewIndexID(spaceID, viewID, activeID)
	engine := &inactivePrepareFailureEngine{
		queryEngine: &queryEngine{stats: viewindex.ViewIndexStats{
			Exists: true, ViewVersion: 1, SchemaHash: "schema-v1", PhysicalBytes: 2,
		}},
		failingIndex: inactiveID,
		indexRows: map[string][]*pb.RowFieldValues{activeID: {{
			Key: &pb.RowKey{SpaceId: spaceID, DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
				SubjectId: "A", Freq: "1m", DataTime: "2026-10-02T00:00:00Z", SeriesTag: "default",
			}}},
		}}},
		removed: map[string]bool{},
	}
	svc := &Service{
		engines:          map[string]viewindex.Engine{"duckdb": engine},
		indexEngine:      make(map[string]string),
		schemas:          make(map[string]viewindex.ViewIndexSchema),
		views:            make(map[viewRef]*viewRuntime),
		catalogViews:     make(map[viewRef]*pb.View),
		indexView:        make(map[string]viewRef),
		byData:           make(map[datasetRef]map[string]struct{}),
		indexGates:       make(map[string]*indexWriteGate),
		indexGeneration:  make(map[string]uint64),
		retiringIndexes:  make(map[string]uint64),
		preparingIndexes: make(map[string]uint64),
		authSecret:       "view-secret",
	}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: spaceID, ViewId: viewID, DatasetId: "prices", Engine: "duckdb",
		ActiveIndexId: activeID, ActiveViewRevision: 1, DesiredViewRevision: 1,
		ActiveViewSchemaHash: "schema-v1", ActiveColumns: columns, Columns: columns,
		Freq: "1m", Status: "active",
	}}
	svc.consumerState = func(context.Context) (jetstream.ConsumerState, error) { return jetstream.ConsumerState{}, nil }
	err := svc.maintainView(ctx, MaintenanceOptions{
		Metadata: metadata, OwnerID: "owner", MaxViewFileBytes: 1,
		Bars:                        5000,
		RebuildMaxPendingConfigured: true, RebuildIdleChecksConfigured: true, RebuildIdleChecks: 1,
	}, svc.internalAuth(), metadata.view)
	if err == nil || !strings.Contains(err.Error(), "injected inactive index prepare failure") {
		t.Fatalf("maintainView error = %v, want injected inactive build failure", err)
	}
	if metadata.claims != 1 || metadata.activated {
		t.Fatalf("inactive build claims=%d activated=%v, want one claim and no activation", metadata.claims, metadata.activated)
	}
	if len(engine.prepared) != 1 || engine.prepared[0] != inactiveID {
		t.Fatalf("prepared indexes before failure = %v, want inactive index %q", engine.prepared, inactiveID)
	}
	active, runtime := svc.activeIndex(spaceID, viewID)
	if active != activeID || runtime == nil {
		t.Fatalf("active slot after failed build = %q runtime=%v, want %q", active, runtime != nil, activeID)
	}
	if metadata.view.GetActiveIndexId() != activeID {
		t.Fatalf("metadata active slot after failed build = %q, want %q", metadata.view.GetActiveIndexId(), activeID)
	}
	query, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		AuthInfo: svc.internalAuth(), SpaceId: spaceID, ViewId: viewID,
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: spaceID, DatasetId: "prices", SubjectId: "A", Freq: "1m"}},
		Limit:     1,
	})
	if err != nil || query.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("query old active index after inactive build failure: rsp=%v err=%v", query, err)
	}
	if len(query.GetRows()) != 1 || query.GetRows()[0].GetKey().GetDataTime() != "2026-10-02T00:00:00Z" {
		t.Fatalf("active index data after failed inactive prepare = %v, want the original active row", query.GetRows())
	}
	if len(engine.queried) != 1 || engine.queried[0] != activeID {
		t.Fatalf("query indexes after inactive build failure = %v, want only active index %q", engine.queried, activeID)
	}
}

type blockedSeriesCapacityReader struct {
	started     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func (r *blockedSeriesCapacityReader) SeriesCapacity(context.Context, string, uint64) (viewindex.SeriesCapacityResult, error) {
	close(r.started)
	<-r.release
	return viewindex.SeriesCapacityResult{Exceeded: true, SubjectID: "subject-a", Rows: 6001}, nil
}

type cancelableSeriesCapacityReader struct {
	mu      sync.Mutex
	started chan struct{}
	calls   int
	active  int
	maxLive int
}

func (r *cancelableSeriesCapacityReader) SeriesCapacity(ctx context.Context, _ string, _ uint64) (viewindex.SeriesCapacityResult, error) {
	r.mu.Lock()
	r.calls++
	r.active++
	if r.active > r.maxLive {
		r.maxLive = r.active
	}
	r.mu.Unlock()
	r.started <- struct{}{}
	<-ctx.Done()
	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return viewindex.SeriesCapacityResult{}, ctx.Err()
}

func TestSeriesCapacityScanTimeoutDoesNotBlockLoopOrOverlapScan(t *testing.T) {
	service := &Service{}
	reader := &blockedSeriesCapacityReader{started: make(chan struct{}), release: make(chan struct{})}
	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	ref := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "view-kline"}, indexID: "index-a"}
	interval, jitter, err := normalizeCapacityCheckSchedule(MaintenanceOptions{CapacityCheckInterval: time.Hour, CapacityCheckJitter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if run, _ := service.beginSeriesCapacityCheck(ref, start, interval, jitter, func(time.Duration) time.Duration { return 0 }); !run {
		t.Fatal("first capacity scan did not start")
	}
	finished := make(chan struct{})
	scanErrors := make(chan error, 1)
	go func() {
		_, scanErr := seriesCapacityWithTimeout(context.Background(), reader, ref.indexID, 6000, 10*time.Millisecond, func(result viewindex.SeriesCapacityResult, scanErr error) {
			service.finishSeriesCapacityCheck(ref, result, scanErr)
			close(finished)
		})
		scanErrors <- scanErr
	}()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("capacity scan did not reach reader")
	}
	select {
	case err := <-scanErrors:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("capacity scan error = %v, want timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed capacity scan blocked the maintenance caller")
	}
	if run, _ := service.beginSeriesCapacityCheck(ref, start.Add(time.Hour), interval, jitter, nil); run {
		t.Fatal("timed-out in-flight scan allowed an overlapping retry")
	}
	reader.releaseOnce.Do(func() { close(reader.release) })
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("detached capacity scan did not finish after the reader was released")
	}
	if run, cached := service.beginSeriesCapacityCheck(ref, start.Add(time.Hour), interval, jitter, nil); !run || !cached.Exceeded {
		t.Fatalf("post-timeout retry = run %v, cached %#v; want one retry and completed offender", run, cached)
	}
}

func TestSeriesCapacityScanTimeoutReleasesGateAfterReaderCancellation(t *testing.T) {
	service := &Service{}
	reader := &cancelableSeriesCapacityReader{started: make(chan struct{}, 2)}
	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	ref := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "view-kline"}, indexID: "index-a"}
	interval, jitter, err := normalizeCapacityCheckSchedule(MaintenanceOptions{CapacityCheckInterval: time.Hour, CapacityCheckJitter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if run, _ := service.beginSeriesCapacityCheck(ref, start, interval, jitter, func(time.Duration) time.Duration { return 0 }); !run {
		t.Fatal("first capacity scan did not start")
	}

	runScan := func() {
		finished := make(chan struct{})
		scanErrors := make(chan error, 1)
		go func() {
			_, scanErr := seriesCapacityWithTimeout(context.Background(), reader, ref.indexID, 6000, 10*time.Millisecond, func(result viewindex.SeriesCapacityResult, scanErr error) {
				service.finishSeriesCapacityCheck(ref, result, scanErr)
				close(finished)
			})
			scanErrors <- scanErr
		}()
		select {
		case <-reader.started:
		case <-time.After(time.Second):
			t.Fatal("capacity scan did not reach the reader")
		}
		select {
		case scanErr := <-scanErrors:
			if !errors.Is(scanErr, context.DeadlineExceeded) {
				t.Fatalf("capacity scan error = %v, want timeout", scanErr)
			}
		case <-time.After(time.Second):
			t.Fatal("timed capacity scan blocked the maintenance caller")
		}
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("reader did not stop after its context was canceled")
		}
	}

	runScan()
	if run, _ := service.beginSeriesCapacityCheck(ref, start.Add(time.Hour), interval, jitter, nil); !run {
		t.Fatal("next hourly capacity scan was not allowed after cancellation")
	}
	runScan()
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.calls != 2 || reader.maxLive != 1 || reader.active != 0 {
		t.Fatalf("reader calls=%d max concurrent=%d active=%d, want 2, 1, 0", reader.calls, reader.maxLive, reader.active)
	}
}

func TestCapacityCheckInventoryDropsDeletedViewsAndStaleIndexes(t *testing.T) {
	svc := &Service{capacityChecks: make(map[capacityCheckRef]*seriesCapacityCheckState)}
	active := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "active"}, indexID: "active-index"}
	staleIndex := capacityCheckRef{viewRef: active.viewRef, indexID: "previous-index"}
	deleted := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "deleted"}, indexID: "deleted-index"}
	svc.capacityChecks[active] = &seriesCapacityCheckState{nextCheck: time.Now().Add(time.Hour)}
	svc.capacityChecks[staleIndex] = &seriesCapacityCheckState{nextCheck: time.Now().Add(time.Hour)}
	svc.capacityChecks[deleted] = &seriesCapacityCheckState{nextCheck: time.Now().Add(time.Hour)}
	svc.permanentCapacityOverLimit = map[capacityCheckRef]struct{}{staleIndex: {}, deleted: {}}

	svc.reconcileCapacityCheckInventory(map[capacityCheckRef]struct{}{active: {}})

	if len(svc.capacityChecks) != 1 {
		t.Fatalf("capacity check states = %d, want only the active View/index state", len(svc.capacityChecks))
	}
	if _, ok := svc.capacityChecks[active]; !ok {
		t.Fatal("active View capacity state was removed")
	}
	if _, ok := svc.capacityChecks[deleted]; ok {
		t.Fatal("deleted View capacity state was retained")
	}
	if _, ok := svc.capacityChecks[staleIndex]; ok {
		t.Fatal("previous active-index capacity state was retained")
	}
	if len(svc.permanentCapacityOverLimit) != 0 {
		t.Fatalf("stale permanent capacity alerts = %d, want 0", len(svc.permanentCapacityOverLimit))
	}

	for index := 0; index < 1000; index++ {
		ref := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: fmt.Sprintf("deleted-%d", index)}, indexID: "index"}
		svc.capacityChecks[ref] = &seriesCapacityCheckState{}
		svc.reconcileCapacityCheckInventory(map[capacityCheckRef]struct{}{active: {}})
	}
	if len(svc.capacityChecks) != 1 {
		t.Fatalf("capacity check states after View churn = %d, want 1", len(svc.capacityChecks))
	}
}

func TestNormalizeCapacityCheckScheduleRejectsJitterBeyondInterval(t *testing.T) {
	_, _, err := normalizeCapacityCheckSchedule(MaintenanceOptions{CapacityCheckInterval: time.Hour, CapacityCheckJitter: 2 * time.Hour})
	if err == nil || !strings.Contains(err.Error(), "capacity_check_jitter") {
		t.Fatalf("normalizeCapacityCheckSchedule error = %v, want capacity_check_jitter validation", err)
	}
}

func TestSeriesCapacityChecksUseIndependentInitialPhasesPerService(t *testing.T) {
	start := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	interval, jitter, err := normalizeCapacityCheckSchedule(MaintenanceOptions{
		CapacityCheckInterval: time.Hour,
		CapacityCheckJitter:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	services := []*Service{{}, {}}
	offsets := []time.Duration{10 * time.Minute, 40 * time.Minute}
	for index, svc := range services {
		ref := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "view-kline"}, indexID: "index-a"}
		source := func(time.Duration) time.Duration { return offsets[index] }
		if run, _ := svc.beginSeriesCapacityCheck(ref, start, interval, jitter, source); run {
			t.Fatalf("service %d scanned synchronously before its random initial phase", index)
		}
	}
	ref := capacityCheckRef{viewRef: viewRef{spaceID: "crypto", viewID: "view-kline"}, indexID: "index-a"}
	if run, _ := services[0].beginSeriesCapacityCheck(ref, start.Add(10*time.Minute), interval, jitter, nil); !run {
		t.Fatal("first service did not scan at its randomized initial phase")
	}
	if run, _ := services[1].beginSeriesCapacityCheck(ref, start.Add(10*time.Minute), interval, jitter, nil); run {
		t.Fatal("second service was synchronized with the first service")
	}
	if run, _ := services[1].beginSeriesCapacityCheck(ref, start.Add(40*time.Minute), interval, jitter, nil); !run {
		t.Fatal("second service did not scan at its own randomized initial phase")
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
		DesiredViewRevision: 1, ActiveViewRevision: 1,
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
	view := &pb.View{DesiredViewRevision: 1, ActiveViewRevision: 1}
	failed := &pb.ViewIndexBuild{UpdatedAt: "2026-08-12T00:00:00Z"}
	if shouldRetryFailedBuild(view, failed, true, time.Date(2026, 8, 12, 0, 29, 59, 0, time.UTC)) {
		t.Fatal("size-limit rebuild retried before cooldown")
	}
	if !shouldRetryFailedBuild(view, failed, true, time.Date(2026, 8, 12, 0, 30, 0, 0, time.UTC)) {
		t.Fatal("size-limit rebuild did not retry after cooldown")
	}
}

func TestFailedBuildMissingActiveIsNotHeldBySizeCooldown(t *testing.T) {
	view := &pb.View{ActiveIndexId: "idx", DesiredViewRevision: 1, ActiveViewRevision: 1}
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
		DatasetId:           "records",
		DesiredViewRevision: 1,
		Columns: []*pb.ViewColumn{{
			SpaceId: "space", ViewId: "records", OriginId: "title", ColumnName: "title",
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
		SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices",
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
		SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices",
		ActiveIndexId: "missing-active", ActiveViewRevision: 1, DesiredViewRevision: 1,
	}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("restore missing active error = %v", err)
	}
}

func TestRestoreActiveViewsSkipsPhysicalContractMismatch(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 1, SchemaHash: "old"}}
	metadata := &maintenanceMetadata{view: &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 2, ActiveViewSchemaHash: "new",
	}}
	if err := svc.RestoreActiveViews(context.Background(), MaintenanceOptions{Metadata: metadata}); err != nil {
		t.Fatalf("restore mismatch should skip, got %v", err)
	}
	indexID, runtime := svc.activeIndex("space", "prices")
	if runtime != nil && strings.TrimSpace(indexID) != "" {
		t.Fatalf("mismatched view attached active=%q", indexID)
	}
}

func TestAttachPendingViewBuildRejectsPhysicalSchemaMismatch(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	column := &pb.ViewColumn{SpaceId: "space", ViewId: "prices", ColumnName: "close", OriginId: "close"}
	svc.engines["bleve"] = &queryEngine{stats: viewindex.ViewIndexStats{Exists: true, ViewVersion: 2, SchemaHash: "wrong"}}
	view := &pb.View{
		SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices",
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
	columns := []*pb.ViewColumn{{SpaceId: "space", ViewId: "records", OriginId: "title", ColumnName: "title"}}
	prepared, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: indexID, Schema: &pb.ViewIndexSchema{SpaceId: "space", ViewId: "records", DatasetId: "records", ViewVersion: 2, Engine: "bleve", ViewSchemaHash: "schema-2", Columns: columns}})
	if err != nil || prepared.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare: rsp=%v err=%v", prepared, err)
	}
	metadata := &maintenanceMetadata{view: &pb.View{SpaceId: "space", ViewId: "records", DatasetId: "records", ActiveIndexId: "records-a", ActiveViewRevision: 1, DesiredViewRevision: 2, Engine: "bleve", ActiveColumns: columns}, activateErr: errors.New("response lost")}
	gotErr := svc.activateViewBuild(ctx, MaintenanceOptions{Metadata: metadata, OwnerID: "storage-view"}, auth, metadata.view, "build-1", indexID, "bleve", 2, "schema-2", columns)
	if gotErr == nil {
		t.Fatal("expected activation retry when readback does not commit")
	}
	// The previous call did not expose an active index because the fake had not
	// committed it. Simulate the Metadata transaction having committed before
	// the response was lost and retry.
	metadata.view.ActiveIndexId = indexID
	if err := svc.activateViewBuild(ctx, MaintenanceOptions{Metadata: metadata, OwnerID: "storage-view"}, auth, metadata.view, "build-1", indexID, "bleve", 2, "schema-2", columns); err != nil {
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
		SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices",
		ActiveIndexId: "prices-a", ActiveViewRevision: 1, DesiredViewRevision: 2,
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
		SpaceId: "space", ViewId: "prices", Engine: "bleve", DatasetId: "prices", DesiredViewRevision: 1,
		Columns: []*pb.ViewColumn{{SpaceId: "space", ViewId: "prices", OriginId: "close", ColumnName: "close"}},
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
		SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "prices",
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
		SpaceId: "space", ViewId: "prices", Engine: "duckdb", DatasetId: "prices",
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

func shouldRetryFailedBuild(view *pb.View, failedBuild *pb.ViewIndexBuild, capacityMaintenanceExceeded bool, now time.Time) bool {
	return shouldRetryFailedBuildWithCause(view, failedBuild, capacityMaintenanceExceeded, capacityMaintenanceExceeded, now)
}

func (s *Service) capacityMaintenanceBuildIdle(ctx context.Context) bool {
	return s.capacityMaintenanceBuildIdleFor(ctx, viewRef{}, capacityMaintenanceBuildBacklogThreshold, 1)
}
