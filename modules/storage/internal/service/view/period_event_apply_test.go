package view

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/client"
)

type periodStateKey struct {
	spaceID, viewID, datasetID, frequency string
	periodTime                            int64
}

type syncPointKey struct {
	spaceID, viewID, datasetID, requestID string
}

type periodMetadataFake struct {
	states       map[periodStateKey]*pb.ViewPeriodDatasetState
	syncPoints   map[syncPointKey]*pb.ViewSyncPoint
	syncAttempts int
}

func newPeriodMetadataFake() *periodMetadataFake {
	return &periodMetadataFake{
		states:     make(map[periodStateKey]*pb.ViewPeriodDatasetState),
		syncPoints: make(map[syncPointKey]*pb.ViewSyncPoint),
	}
}

func (m *periodMetadataFake) UpsertViewPeriodDatasetState(_ context.Context, req *pb.UpsertViewPeriodDatasetStateReq, _ ...client.Option) (*pb.UpsertViewPeriodDatasetStateRsp, error) {
	state := req.GetState()
	key := periodStateKey{state.GetSpaceId(), state.GetViewId(), state.GetDatasetId(), state.GetFrequency(), state.GetPeriodTime()}
	if existing := m.states[key]; existing != nil {
		return &pb.UpsertViewPeriodDatasetStateRsp{RetInfo: successRetInfo(), State: proto.Clone(existing).(*pb.ViewPeriodDatasetState)}, nil
	}
	m.states[key] = proto.Clone(state).(*pb.ViewPeriodDatasetState)
	return &pb.UpsertViewPeriodDatasetStateRsp{RetInfo: successRetInfo(), State: proto.Clone(state).(*pb.ViewPeriodDatasetState)}, nil
}

func (m *periodMetadataFake) ListViewPeriodDatasetStates(_ context.Context, req *pb.ListViewPeriodDatasetStatesReq, _ ...client.Option) (*pb.ListViewPeriodDatasetStatesRsp, error) {
	var states []*pb.ViewPeriodDatasetState
	for key, state := range m.states {
		if key.spaceID == req.GetSpaceId() && key.viewID == req.GetViewId() && key.frequency == req.GetFrequency() && key.periodTime == req.GetPeriodTime() {
			states = append(states, proto.Clone(state).(*pb.ViewPeriodDatasetState))
		}
	}
	sort.Slice(states, func(i, j int) bool { return states[i].GetDatasetId() < states[j].GetDatasetId() })
	return &pb.ListViewPeriodDatasetStatesRsp{RetInfo: successRetInfo(), States: states}, nil
}

func (m *periodMetadataFake) RecordViewSyncPoint(_ context.Context, req *pb.RecordViewSyncPointReq, _ ...client.Option) (*pb.RecordViewSyncPointRsp, error) {
	m.syncAttempts++
	point := req.GetSyncPoint()
	key := syncPointKey{point.GetSpaceId(), point.GetViewId(), point.GetDatasetId(), point.GetRequestId()}
	if existing := m.syncPoints[key]; existing != nil {
		if !proto.Equal(existing, point) {
			return nil, errors.New("view sync point conflict")
		}
		return &pb.RecordViewSyncPointRsp{RetInfo: successRetInfo(), SyncPoint: proto.Clone(existing).(*pb.ViewSyncPoint)}, nil
	}
	m.syncPoints[key] = proto.Clone(point).(*pb.ViewSyncPoint)
	return &pb.RecordViewSyncPointRsp{RetInfo: successRetInfo(), SyncPoint: proto.Clone(point).(*pb.ViewSyncPoint)}, nil
}

type publishedReady struct {
	event   events.Event
	payload proto.Message
	opts    events.PublishOptions
}

type readyPublisherFake struct {
	attempts []publishedReady
	byID     map[string]publishedReady
}

func newReadyPublisherFake() *readyPublisherFake {
	return &readyPublisherFake{byID: make(map[string]publishedReady)}
}

func (p *readyPublisherFake) Publish(_ context.Context, event events.Event, payload proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	item := publishedReady{event: event, payload: proto.Clone(payload), opts: opts}
	p.attempts = append(p.attempts, item)
	if _, exists := p.byID[opts.EventID]; exists {
		return &jetstream.PublishAck{Stream: event.Stream(), Duplicate: true}, nil
	}
	p.byID[opts.EventID] = item
	return &jetstream.PublishAck{Stream: event.Stream(), Sequence: uint64(len(p.byID))}, nil
}

func newPeriodTestService(metadata PeriodMetadataClient, publisher ReadyEventPublisher, views ...*pb.View) *Service {
	service := &Service{
		authSecret:     "period-test-secret",
		views:          make(map[viewRef]*viewRuntime),
		catalogViews:   make(map[viewRef]*pb.View),
		indexRevision:  make(map[string]uint64),
		appliedFence:   make(map[appliedFenceKey]uint64),
		periodMetadata: metadata,
		readyPublisher: publisher,
	}
	for _, view := range views {
		key := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
		service.catalogViews[key] = proto.Clone(view).(*pb.View)
		service.views[key] = &viewRuntime{active: view.GetActiveIndexId()}
		service.indexRevision[view.GetActiveIndexId()] = 1
	}
	return service
}

func periodMessage(eventID string, occurredAt time.Time) *eventpb.EventMessage {
	return &eventpb.EventMessage{EventId: eventID, SpaceId: "quant", OccurredAt: timestamppb.New(occurredAt.UTC())}
}

func collectorCompleted(dataset, status string, subjects, failed []string, at time.Time, periodTime int64) *storageeventpb.CollectorPeriodCompleted {
	return &storageeventpb.CollectorPeriodCompleted{
		DatasetId: dataset, Frequency: "1m", PeriodTime: periodTime, Status: status,
		BatchId: "batch-" + dataset, ConfigSnapshotId: "cfg-1", ExpectedScopeRef: "universe:" + dataset + ":1m",
		UniverseSubjectIds: subjects, FailedSubjects: failed,
		CommittedPositions: []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 1}},
		CollectedAt:        timestamppb.New(at),
	}
}

func TestHandleCollectorPeriodCompletedPublishesSingleDatasetIdempotently(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	periodTime := int64(1786032000)
	otherAt := time.Date(2026, 8, 7, 0, 0, 1, 0, time.UTC)
	if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("fundamentals-ready", otherAt), collectorCompleted("fundamentals", "complete", []string{"ETH-USDT"}, nil, otherAt, periodTime)); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 0 {
		t.Fatalf("ready published for an unrelated dataset: %d", len(publisher.attempts))
	}

	secondAt := otherAt.Add(time.Second)
	message := periodMessage("prices-ready", secondAt)
	payload := collectorCompleted("prices", "degraded", []string{"ETH-USDT", "BTC-USDT", "BTC-USDT"}, []string{"ETH-USDT"}, secondAt, periodTime)
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 1)
	if err := service.HandleCollectorPeriodCompleted(context.Background(), message, payload); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleCollectorPeriodCompleted(context.Background(), message, payload); err != nil {
		t.Fatalf("idempotent marker retry failed: %v", err)
	}
	if len(metadata.states) != 1 || len(publisher.attempts) != 2 || len(publisher.byID) != 1 {
		t.Fatalf("states=%d publish attempts=%d unique events=%d", len(metadata.states), len(publisher.attempts), len(publisher.byID))
	}
	if publisher.attempts[0].opts.EventID != publisher.attempts[1].opts.EventID {
		t.Fatal("marker retry changed the ready event ID")
	}
	ready, ok := publisher.attempts[0].payload.(*storageeventpb.ViewDataReady)
	if !ok || publisher.attempts[0].event.Name() != events.ViewDataReady.Name() {
		t.Fatalf("published event=%s payload=%T", publisher.attempts[0].event.Name(), publisher.attempts[0].payload)
	}
	if ready.GetViewId() != "source-view" || ready.GetStatus() != "degraded" || ready.GetDatasetId() != "prices" || ready.GetFailedScopeRef() != "ETH-USDT" {
		t.Fatalf("ready payload=%v", ready)
	}
	if ready.GetCompletionKind() != events.CollectorPeriodCompleted.Name() {
		t.Fatalf("completion_kind=%q", ready.GetCompletionKind())
	}
	if ready.GetViewConfigId() == "" || ready.GetVisibleScope() == "" || len(ready.GetCommittedPositions()) == 0 {
		t.Fatalf("ready identity incomplete=%v", ready)
	}
	got := uniqueSortedStrings(append([]string(nil), ready.GetUniverseSubjectIds()...))
	want := uniqueSortedStrings([]string{"BTC-USDT", "ETH-USDT"})
	if len(got) != len(want) {
		t.Fatalf("universe_subject_ids=%v", ready.GetUniverseSubjectIds())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("universe_subject_ids=%v", ready.GetUniverseSubjectIds())
		}
	}
}

// Collector may report the same period again (for example after a restart)
// with a new event ID. The first report decided the period; the re-report must
// succeed without republishing so the Dataset's ordered lane keeps moving.
func TestHandleCollectorPeriodCompletedAcceptsReReportedPeriod(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	periodTime := int64(1786032000)
	firstAt := time.Date(2026, 8, 7, 0, 0, 1, 0, time.UTC)
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 1)
	if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("prices-ready", firstAt),
		collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, firstAt, periodTime)); err != nil {
		t.Fatal(err)
	}
	reportedAt := firstAt.Add(11 * time.Minute)
	if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("prices-ready-again", reportedAt),
		collectorCompleted("prices", "degraded", []string{"BTC-USDT", "ETH-USDT"}, []string{"ETH-USDT"}, reportedAt, periodTime)); err != nil {
		t.Fatalf("re-reported period must not fail: %v", err)
	}
	if len(metadata.states) != 1 || len(publisher.attempts) != 1 {
		t.Fatalf("states=%d publish attempts=%d, want the first report only", len(metadata.states), len(publisher.attempts))
	}
}

func TestHandleCollectorPeriodCompletedIgnoresExtraRuntimeDatasets(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	runtime := service.views[viewRef{spaceID: "quant", viewID: "source-view"}]
	runtime.mu.Lock()
	runtime.activeDatasetIDs = []string{"prices", "fundamentals"}
	runtime.activePrimaryDatasetID = "prices"
	runtime.activeDatasetSet = true
	runtime.mu.Unlock()

	periodTime := int64(1786032000)
	at := time.Date(2026, 8, 7, 0, 0, 1, 0, time.UTC)
	message := periodMessage("prices-ready", at)
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 1)
	if err := service.HandleCollectorPeriodCompleted(context.Background(), message, collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, periodTime)); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 1 {
		t.Fatalf("single dataset view should publish without auxiliary sources: attempts=%d", len(publisher.attempts))
	}
}

func TestHandleFactorPeriodComputedPublishesResultViewReady(t *testing.T) {
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(nil, publisher, &pb.View{
		SpaceId: "quant", ViewId: "result-view", DatasetId: "factor-results", ActiveIndexId: "result-view-a",
	})
	occurredAt := time.Date(2026, 8, 7, 0, 1, 0, 0, time.UTC)
	message := periodMessage("factor-marker-1", occurredAt)
	payload := &storageeventpb.FactorPeriodComputed{
		DatasetId: "factor-results", Frequency: "1m", PeriodTime: 1786032000, Status: "degraded",
		SourceDatasetId: "prices", UniverseSubjectIds: []string{"ETH-USDT", "BTC-USDT"}, FailedSubjects: []string{"BTC-USDT"},
		Factors: []*storageeventpb.FactorPeriodState{
			{FactorId: "z-factor", Status: "degraded", FailedSubjects: []string{"ETH-USDT", "BTC-USDT"}, SourceHash: "hash-z", DefinitionHash: "def-z"},
			{FactorId: "a-factor", Status: "complete", SourceHash: "hash-a", DefinitionHash: "def-a"},
		},
		ComputedAt: timestamppb.New(occurredAt), TriggerEventId: "source-ready-1",
	}
	if err := service.HandleFactorPeriodComputed(context.Background(), message, payload); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleFactorPeriodComputed(context.Background(), message, payload); err != nil {
		t.Fatalf("idempotent factor marker retry failed: %v", err)
	}
	if len(publisher.attempts) != 2 || len(publisher.byID) != 1 || publisher.attempts[0].opts.EventID != publisher.attempts[1].opts.EventID {
		t.Fatalf("publish attempts=%d unique=%d", len(publisher.attempts), len(publisher.byID))
	}
	ready, ok := publisher.attempts[0].payload.(*storageeventpb.ViewDataReady)
	if !ok || publisher.attempts[0].event.Name() != events.ViewDataReady.Name() {
		t.Fatalf("published event=%s payload=%T", publisher.attempts[0].event.Name(), publisher.attempts[0].payload)
	}
	if ready.GetViewId() != "result-view" || ready.GetDatasetId() != "factor-results" || ready.GetStatus() != "degraded" || ready.GetFailedScopeRef() != "BTC-USDT,ETH-USDT" {
		t.Fatalf("factor ready payload=%v", ready)
	}
	if ready.GetCompletionKind() != events.FactorPeriodComputed.Name() {
		t.Fatalf("completion_kind=%q", ready.GetCompletionKind())
	}
	if got := ready.GetUniverseSubjectIds(); !reflect.DeepEqual(got, payload.GetUniverseSubjectIds()) {
		t.Fatalf("factor universe_subject_ids=%v", got)
	}
	if len(ready.GetFactors()) != len(payload.GetFactors()) {
		t.Fatalf("factor count=%d, want %d", len(ready.GetFactors()), len(payload.GetFactors()))
	}
	for i, state := range payload.GetFactors() {
		if !proto.Equal(state, ready.GetFactors()[i]) {
			t.Fatalf("factor %d changed: got=%v want=%v", i, ready.GetFactors()[i], state)
		}
	}
	payload.Factors[0].FailedSubjects[0] = "changed-after-handling"
	if ready.Factors[0].FailedSubjects[0] != "ETH-USDT" {
		t.Fatal("ready factor state aliases input payload")
	}
}

func TestHandleFactorPeriodComputedEmptyFactorsPublishesDegradedReady(t *testing.T) {
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(nil, publisher, &pb.View{
		SpaceId: "quant", ViewId: "result-view", DatasetId: "factor-results", ActiveIndexId: "result-view-a",
	})
	occurredAt := time.Date(2026, 8, 7, 0, 1, 0, 0, time.UTC)
	payload := &storageeventpb.FactorPeriodComputed{
		DatasetId: "factor-results", Frequency: "1m", PeriodTime: 1786032000, Status: "degraded",
		SourceDatasetId: "prices", UniverseSubjectIds: []string{"ETH-USDT"}, FailedSubjects: []string{"ETH-USDT"},
	}
	if err := service.HandleFactorPeriodComputed(context.Background(), periodMessage("factor-empty", occurredAt), payload); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 1 {
		t.Fatalf("empty degraded factors did not publish ViewDataReady: %d", len(publisher.attempts))
	}
	ready := publisher.attempts[0].payload.(*storageeventpb.ViewDataReady)
	if len(ready.GetFactors()) != 0 || ready.GetStatus() != "degraded" || ready.GetFailedScopeRef() != "ETH-USDT" {
		t.Fatalf("empty factors ready=%v", ready)
	}
}

func TestHandleFactorPeriodComputedViewPreservesFactorUniverse(t *testing.T) {
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(nil, publisher, &pb.View{
		SpaceId: "quant", ViewId: "factor-result", DatasetId: "factor-results", ActiveIndexId: "factor-result-a", Freq: "1m",
	})
	at := time.Date(2026, 8, 7, 0, 1, 0, 0, time.UTC)
	payload := &storageeventpb.FactorPeriodComputed{
		DatasetId: "factor-results", SourceDatasetId: "prices", Frequency: "1m", PeriodTime: at.Unix(), Status: "degraded",
		UniverseSubjectIds: []string{"BTC-USDT", "ETH-USDT"}, FailedSubjects: []string{"ETH-USDT"},
		Factors: []*storageeventpb.FactorPeriodState{{FactorId: "factor", Status: "degraded", FailedSubjects: []string{"ETH-USDT"}, SourceHash: "hash", DefinitionHash: "def"}},
	}
	if err := service.HandleFactorPeriodComputed(context.Background(), periodMessage("factor-result", at), payload); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 1 {
		t.Fatalf("publish attempts=%d", len(publisher.attempts))
	}
	attempt := publisher.attempts[0]
	ready := attempt.payload.(*storageeventpb.ViewDataReady)
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Encode(attempt.event, ready, attempt.opts); err != nil {
		t.Fatalf("factor ready violates event contract: %v", err)
	}
	if ready.GetVisibleScope() != "view:factor-result" || !reflect.DeepEqual(ready.GetUniverseSubjectIds(), payload.GetUniverseSubjectIds()) {
		t.Fatalf("factor ready scope/universe=%v", ready)
	}
}

func TestHandleCollectorPeriodCompletedKeepsObjectUniverse(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "kline-view", DatasetId: "prices", ActiveIndexId: "kline-a", Freq: "1m",
	})
	service.NoteAppliedPosition("quant", "kline-view", "kline-a", "node-a", "store-a", 1)
	at := time.Date(2026, 9, 13, 16, 2, 0, 0, time.UTC)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT", "ETH-USDT"}, nil, at, at.Unix())
	if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("kline-ready", at), payload); err != nil {
		t.Fatal(err)
	}
	ready := publisher.attempts[0].payload.(*storageeventpb.ViewDataReady)
	if got := ready.GetUniverseSubjectIds(); len(got) != 2 || got[0] != "BTC-USDT" || got[1] != "ETH-USDT" {
		t.Fatalf("a View must keep the completion universe: %v", got)
	}
}

func TestHandleDatasetSyncPointRecordsEveryDependentViewIdempotently(t *testing.T) {
	metadata := newPeriodMetadataFake()
	service := newPeriodTestService(metadata, nil,
		&pb.View{SpaceId: "quant", ViewId: "view-a", DatasetId: "prices", ActiveIndexId: "view-a-index"},
		&pb.View{SpaceId: "quant", ViewId: "view-b", DatasetId: "prices", ActiveIndexId: "view-b-index"},
		&pb.View{SpaceId: "quant", ViewId: "inactive", DatasetId: "prices"},
	)
	occurredAt := time.Date(2026, 8, 7, 0, 2, 0, 0, time.UTC)
	message := periodMessage("sync-marker-1", occurredAt)
	payload := &storageeventpb.DatasetSyncPoint{SyncPointId: "sync-1", RequestId: "import-1", DatasetId: "prices", Source: "import"}
	if err := service.HandleDatasetSyncPoint(context.Background(), message, payload); err != nil {
		t.Fatal(err)
	}
	if err := service.HandleDatasetSyncPoint(context.Background(), message, payload); err != nil {
		t.Fatalf("idempotent sync-point retry failed: %v", err)
	}
	if len(metadata.syncPoints) != 2 || metadata.syncAttempts != 4 {
		t.Fatalf("sync points=%d attempts=%d", len(metadata.syncPoints), metadata.syncAttempts)
	}
	for _, viewID := range []string{"view-a", "view-b"} {
		point := metadata.syncPoints[syncPointKey{"quant", viewID, "prices", "import-1"}]
		if point == nil || point.GetSyncPointId() != "sync-1" || !point.GetAppliedAt().AsTime().Equal(occurredAt) {
			t.Fatalf("sync point for %s=%v", viewID, point)
		}
	}
}

func TestHandleDatasetSyncPointRecordsBuildingOnlyView(t *testing.T) {
	metadata := newPeriodMetadataFake()
	service := newPeriodTestService(metadata, nil, &pb.View{
		SpaceId: "quant", ViewId: "building-view", DatasetId: "prices",
	})
	service.views[viewRef{spaceID: "quant", viewID: "building-view"}].next = "building-index"
	message := periodMessage("sync-building-1", time.Date(2026, 8, 7, 0, 3, 0, 0, time.UTC))
	payload := &storageeventpb.DatasetSyncPoint{SyncPointId: "sync-building", RequestId: "import-building", DatasetId: "prices", Source: "import"}
	if err := service.HandleDatasetSyncPoint(context.Background(), message, payload); err != nil {
		t.Fatal(err)
	}
	if len(metadata.syncPoints) != 1 {
		t.Fatalf("building-only sync points = %d, want 1", len(metadata.syncPoints))
	}
}

// flakyReadyPublisher 让前 failures 次发布失败，之后委托给 inner。
type flakyReadyPublisher struct {
	inner    *readyPublisherFake
	failures int
}

func (p *flakyReadyPublisher) Publish(ctx context.Context, event events.Event, payload proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	if p.failures > 0 {
		p.failures--
		return nil, errors.New("eventbus unavailable")
	}
	return p.inner.Publish(ctx, event, payload, opts)
}

// 发布失败时，这一条和排在它后面、尚未尝试的待发布事件都要保留，下次按原顺序重试，不能丢。
func TestViewDataReadyPublishFailureKeepsLaterPendingEvents(t *testing.T) {
	metadata := newPeriodMetadataFake()
	inner := newReadyPublisherFake()
	publisher := &flakyReadyPublisher{inner: inner}
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	for i, id := range []string{"prices-ready-a", "prices-ready-b", "prices-ready-c"} {
		at := time.Date(2026, 9, 13, 16, i, 0, 0, time.UTC)
		payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, at.Unix())
		payload.CommittedPositions = []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 12}}
		if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage(id, at), payload); !errors.Is(err, ErrViewDataReadyPending) {
			t.Fatalf("%s 应等待行写入：%v", id, err)
		}
	}
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
	publisher.failures = 1
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err == nil {
		t.Fatal("发布失败应返回错误")
	}
	if len(inner.byID) != 0 {
		t.Fatalf("第一条失败时不应发布后续事件：%d", len(inner.byID))
	}
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
		t.Fatal(err)
	}
	if len(inner.byID) != 3 {
		t.Fatalf("重试后三条事件都应发布，实际 %d 条", len(inner.byID))
	}
}

// enqueuingFailPublisher 在第一次发布时让另一个协程入队下一根（它的刷新会等当前刷新结束），然后发布失败。
// 失败项与其后的项一直留在队列里，新入队的一根排在它们之后。
type enqueuingFailPublisher struct {
	inner   *readyPublisherFake
	enqueue func()
	failed  bool
}

func (p *enqueuingFailPublisher) Publish(ctx context.Context, event events.Event, payload proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	if !p.failed {
		p.failed = true
		p.enqueue()
		return nil, errors.New("eventbus unavailable")
	}
	return p.inner.Publish(ctx, event, payload, opts)
}

// 发布失败后，失败项与其后的项按原顺序回到队首，刷新期间新入队的一根排在它们之后，之后按周期顺序发布。
func TestViewDataReadyRestoresFailedEventsBeforeNewOnes(t *testing.T) {
	metadata := newPeriodMetadataFake()
	inner := newReadyPublisherFake()
	publisher := &enqueuingFailPublisher{inner: inner}
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	completed := func(id string, minute int) (*eventpb.EventMessage, *storageeventpb.CollectorPeriodCompleted) {
		at := time.Date(2026, 9, 13, 16, minute, 0, 0, time.UTC)
		payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, at.Unix())
		payload.CommittedPositions = []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 12}}
		return periodMessage(id, at), payload
	}
	for i, id := range []string{"prices-ready-1", "prices-ready-2"} {
		message, payload := completed(id, i)
		if err := service.HandleCollectorPeriodCompleted(context.Background(), message, payload); !errors.Is(err, ErrViewDataReadyPending) {
			t.Fatalf("%s 应等待行写入：%v", id, err)
		}
	}
	var wg sync.WaitGroup
	publisher.enqueue = func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			message, payload := completed("prices-ready-3", 2)
			_ = service.HandleCollectorPeriodCompleted(context.Background(), message, payload)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if service.pendingReadyContains("prices-ready-3") {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err == nil {
		t.Fatal("发布失败应返回错误")
	}
	wg.Wait()
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
		t.Fatal(err)
	}
	var order []int64
	for _, attempt := range inner.attempts {
		order = append(order, attempt.payload.(*storageeventpb.ViewDataReady).GetPeriodTime())
	}
	if len(order) != 3 || !(order[0] < order[1] && order[1] < order[2]) {
		t.Fatalf("重试后应按周期顺序发布：%v", order)
	}
}

// readyHangingPublisher 模拟确认丢失：发布一直挂起，直到调用方的截止时间到达（与 PublishRaw 的超时语义一致）。
type readyHangingPublisher struct {
	started chan string
	release chan struct{}
}

func (p *readyHangingPublisher) Publish(ctx context.Context, _ events.Event, _ proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	p.started <- opts.EventID
	select {
	case <-ctx.Done():
		return nil, errors.Join(jetstream.ErrPublishTimeout, ctx.Err())
	case <-p.release:
		return &jetstream.PublishAck{Sequence: 1}, nil
	}
}

func readyTestPeriod(t *testing.T, service *Service, id string, minute int) {
	t.Helper()
	at := time.Date(2026, 9, 13, 16, minute, 0, 0, time.UTC)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, at.Unix())
	payload.CommittedPositions = []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 12}}
	if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage(id, at), payload); !errors.Is(err, ErrViewDataReadyPending) {
		t.Fatalf("%s 应等待行写入：%v", id, err)
	}
}

// 发布途中进程退出不能丢事件：发布成功之前，正在发布的事件仍在落盘的队列里，成功后才移除。
func TestViewDataReadyKeepsInFlightEventPersisted(t *testing.T) {
	publisher := &readyHangingPublisher{started: make(chan string, 1), release: make(chan struct{})}
	service := newPeriodTestService(newPeriodMetadataFake(), publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	service.readyFenceDir = t.TempDir()
	readyTestPeriod(t, service, "prices-ready-1", 0)
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
	done := make(chan error, 1)
	go func() { done <- service.FlushViewDataReady(context.Background(), "quant", "source-view") }()
	eventID := <-publisher.started
	// 刷新途中另一根入队会用内存队列覆盖落盘文件：正在发布的事件不能因此从文件里消失。
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		at := time.Date(2026, 9, 13, 16, 1, 0, 0, time.UTC)
		payload := collectorCompleted("prices", "complete", []string{"BTC-USDT"}, nil, at, at.Unix())
		payload.CommittedPositions = []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 99}}
		_ = service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("prices-ready-2", at), payload)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		service.pendingReadyMu.Lock()
		queued := len(service.pendingReady)
		service.pendingReadyMu.Unlock()
		if queued >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("第二根没有入队")
		}
		time.Sleep(time.Millisecond)
	}
	persisted := func() string {
		raw, err := os.ReadFile(filepath.Join(service.readyFenceDir, "pending.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if !strings.Contains(persisted(), eventID) {
		t.Fatalf("正在发布的事件 %s 应仍在落盘队列中：%s", eventID, persisted())
	}
	close(publisher.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if strings.Contains(persisted(), eventID) || service.pendingReadyContains(eventID) {
		t.Fatalf("发布成功后应移出队列：%s", persisted())
	}
}

// 确认丢失时发布不能一直挂起：超时后返回错误，事件留在队列里，在刷新锁之外重连发布器的专用连接；
// 退避期内的刷新直接跳过，不再每次都付出一次超时，退避结束后照常重试。
func TestViewDataReadyPublishTimesOutAndReconnects(t *testing.T) {
	previousTimeout, previousBackoff := readyPublishTimeout, readyRetryBackoff
	readyPublishTimeout, readyRetryBackoff = 50*time.Millisecond, 300*time.Millisecond
	defer func() { readyPublishTimeout, readyRetryBackoff = previousTimeout, previousBackoff }()
	publisher := &readyHangingPublisher{started: make(chan string, 4), release: make(chan struct{})}
	service := newPeriodTestService(newPeriodMetadataFake(), publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	var reconnects atomic.Int32
	service.readyReconnect = func(context.Context) error {
		reconnects.Add(1)
		return nil
	}
	readyTestPeriod(t, service, "prices-ready-1", 0)
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
	started := time.Now()
	err := service.FlushViewDataReady(context.Background(), "quant", "source-view")
	if !errors.Is(err, jetstream.ErrPublishTimeout) {
		t.Fatalf("应返回发布超时：%v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("发布超时后应尽快返回：%s", elapsed)
	}
	eventID := <-publisher.started
	if !service.pendingReadyContains(eventID) {
		t.Fatalf("超时的事件 %s 应留在队列中等待重试", eventID)
	}
	deadline := time.Now().Add(2 * time.Second)
	for reconnects.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if reconnects.Load() != 1 {
		t.Fatalf("发布超时后应在后台重连一次：%d", reconnects.Load())
	}
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
		t.Fatalf("退避期内的刷新应直接跳过：%v", err)
	}
	select {
	case id := <-publisher.started:
		t.Fatalf("退避期内不应再发布：%s", id)
	default:
	}
	time.Sleep(readyRetryBackoff)
	close(publisher.release)
	if err := service.FlushViewDataReady(context.Background(), "quant", "source-view"); err != nil {
		t.Fatalf("退避结束后应照常发布：%v", err)
	}
	if service.pendingReadyContains(eventID) {
		t.Fatal("发布成功后应移出队列")
	}
}

// 行已经写入：就绪事件发布失败只记日志，留在队列里稍后补发，行事件不能因此失败重投。
func TestRowDeliveryIgnoresReadyPublishFailure(t *testing.T) {
	previousTimeout := readyPublishTimeout
	readyPublishTimeout = 20 * time.Millisecond
	defer func() { readyPublishTimeout = previousTimeout }()
	publisher := &readyHangingPublisher{started: make(chan string, 4), release: make(chan struct{})}
	service := newPeriodTestService(newPeriodMetadataFake(), publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	readyTestPeriod(t, service, "prices-ready-1", 0)
	service.NoteAppliedPosition("quant", "source-view", "source-view-a", "node-a", "store-a", 12)
	service.flushReadyAfterRows(context.Background(), "quant")
	if !service.hasPendingReady() {
		t.Fatal("发布失败的就绪事件应留在队列里")
	}
}
