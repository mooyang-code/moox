package view

import (
	"context"
	"errors"
	"sort"
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
		if !proto.Equal(existing, state) {
			return nil, errors.New("view period dataset state conflict")
		}
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
	service.NoteAppliedPosition("quant", "result-view", "result-view-a", "node-a", "store-a", 2)
	payload := &storageeventpb.FactorPeriodComputed{
		DatasetId: "factor-results", Frequency: "1m", PeriodTime: 1786032000, Status: "degraded",
		BatchId: "batch-1", ConfigSnapshotId: "cfg-1", ExpectedScopeRef: "universe:factor:1m", UniverseSubjectIds: []string{"ETH-USDT"},
		Bindings:           []*storageeventpb.FactorBindingPeriodState{{BindingId: "binding-1", FactorId: "factor-1", Status: "degraded", FailedSubjects: []string{"ETH-USDT"}, SourceHash: "hash-1"}},
		CommittedPositions: []*storageeventpb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 2}},
		ComputedAt:         timestamppb.New(occurredAt), TriggerEventId: "source-ready-1",
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
	if ready.GetViewId() != "result-view" || ready.GetDatasetId() != "factor-results" || ready.GetStatus() != "degraded" || ready.GetFailedScopeRef() != "ETH-USDT" {
		t.Fatalf("factor ready payload=%v", ready)
	}
	if ready.GetCompletionKind() != events.FactorPeriodComputed.Name() {
		t.Fatalf("completion_kind=%q", ready.GetCompletionKind())
	}
	if got := ready.GetUniverseSubjectIds(); len(got) != 1 || got[0] != "ETH-USDT" {
		t.Fatalf("factor universe_subject_ids=%v", got)
	}
	if len(ready.GetBindings()) != 1 || ready.GetBindings()[0].GetBindingId() != "binding-1" || ready.GetBindings()[0].GetStatus() != "degraded" {
		t.Fatalf("factor bindings=%v", ready.GetBindings())
	}
}

func TestHandleFactorPeriodComputedEmptyPositionsDoesNotPublishReady(t *testing.T) {
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(nil, publisher, &pb.View{
		SpaceId: "quant", ViewId: "result-view", DatasetId: "factor-results", ActiveIndexId: "result-view-a",
	})
	occurredAt := time.Date(2026, 8, 7, 0, 1, 0, 0, time.UTC)
	payload := &storageeventpb.FactorPeriodComputed{
		DatasetId: "factor-results", Frequency: "1m", PeriodTime: 1786032000, Status: "degraded",
		UniverseSubjectIds: []string{"ETH-USDT"},
		Bindings:           []*storageeventpb.FactorBindingPeriodState{{BindingId: "binding-1", Status: "degraded", SkippedSubjects: []string{"ETH-USDT"}}},
	}
	if err := service.HandleFactorPeriodComputed(context.Background(), periodMessage("factor-empty", occurredAt), payload); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 0 {
		t.Fatalf("empty committed positions published ViewDataReady: %d", len(publisher.attempts))
	}
}

func TestHandleMergePeriodCompletedEmptyPositionsDoesNotPublishReady(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "source-view", DatasetId: "prices", ActiveIndexId: "source-view-a",
	})
	at := time.Date(2026, 9, 13, 16, 0, 0, 0, time.UTC)
	payload := &storageeventpb.MergePeriodCompleted{
		DatasetId: "prices", Frequency: "1m", PeriodTime: at.Unix(), Status: "degraded",
		UniverseSubjectIds: []string{"BTC-USDT"}, FailedSubjects: []string{"BTC-USDT"},
	}
	if err := service.HandleMergePeriodCompleted(context.Background(), periodMessage("merge-empty", at), payload); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 0 {
		t.Fatalf("empty merge positions published ViewDataReady: %d", len(publisher.attempts))
	}
}

func TestHandleCollectorPeriodCompletedFilteredViewOmitsObjectUniverse(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "spot-view", DatasetId: "prices", ActiveIndexId: "spot-a", FilterJson: `{"market":"spot"}`,
	})
	service.NoteAppliedPosition("quant", "spot-view", "spot-a", "node-a", "store-a", 1)
	at := time.Date(2026, 9, 13, 16, 2, 0, 0, time.UTC)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT", "ETH-USDT"}, nil, at, at.Unix())
	if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("spot-ready", at), payload); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 1 {
		t.Fatalf("filtered ready attempts=%d", len(publisher.attempts))
	}
	ready := publisher.attempts[0].payload.(*storageeventpb.ViewDataReady)
	if len(ready.GetUniverseSubjectIds()) != 0 {
		t.Fatalf("object-filtered View must not copy the full completion universe: %v", ready.GetUniverseSubjectIds())
	}
}

func TestHandleCollectorPeriodCompletedFreqFilterKeepsObjectUniverse(t *testing.T) {
	metadata := newPeriodMetadataFake()
	publisher := newReadyPublisherFake()
	service := newPeriodTestService(metadata, publisher, &pb.View{
		SpaceId: "quant", ViewId: "kline-view", DatasetId: "prices", ActiveIndexId: "kline-a", FilterJson: `{"freq":"1m"}`,
	})
	service.NoteAppliedPosition("quant", "kline-view", "kline-a", "node-a", "store-a", 1)
	at := time.Date(2026, 9, 13, 16, 2, 0, 0, time.UTC)
	payload := collectorCompleted("prices", "complete", []string{"BTC-USDT", "ETH-USDT"}, nil, at, at.Unix())
	if err := service.HandleCollectorPeriodCompleted(context.Background(), periodMessage("kline-ready", at), payload); err != nil {
		t.Fatal(err)
	}
	ready := publisher.attempts[0].payload.(*storageeventpb.ViewDataReady)
	if got := ready.GetUniverseSubjectIds(); len(got) != 2 || got[0] != "BTC-USDT" || got[1] != "ETH-USDT" {
		t.Fatalf("freq-only filter should keep the completion universe: %v", got)
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
