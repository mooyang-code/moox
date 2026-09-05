package outbox

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type runtimeTestStore struct {
	mu        sync.Mutex
	row       domain.OutboxMessage
	published bool
}

func (s *runtimeTestStore) ListPendingOutbox(context.Context, int) ([]domain.OutboxMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.published || s.row.MessageID == "" {
		return nil, nil
	}
	return []domain.OutboxMessage{s.row}, nil
}
func (s *runtimeTestStore) DeleteOutbox(context.Context, string) error {
	s.mu.Lock()
	s.published = true
	s.mu.Unlock()
	return nil
}
func (s *runtimeTestStore) PendingOutboxStats(context.Context) (domain.OutboxStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.published || s.row.MessageID == "" {
		return domain.OutboxStats{}, nil
	}
	return domain.OutboxStats{PendingCount: 1, OldestPending: s.row.CreatedAt}, nil
}
func (s *runtimeTestStore) isPublished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.published
}

type runtimeTestClient struct {
	ready atomic.Bool
	ids   chan string
}

func newRuntimeTestClient() *runtimeTestClient {
	client := &runtimeTestClient{ids: make(chan string, 4)}
	client.ready.Store(true)
	return client
}
func (c *runtimeTestClient) Ready() bool                    { return c.ready.Load() }
func (c *runtimeTestClient) Close() error                   { c.ready.Store(false); return nil }
func (c *runtimeTestClient) EventPublisher() EventPublisher { return runtimeEventPublisher{client: c} }

type runtimeEventPublisher struct {
	client *runtimeTestClient
}

func strategyEventData(id string) ([]byte, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	bar := timestamppb.New(now)
	validUntil := timestamppb.New(now.Add(time.Hour))
	return registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: id, InstanceId: "runner-1", StrategyId: "strategy-1", SessionId: "session-1", LogicalAccountId: "logical-1", BarEndTime: bar, EffectiveAt: bar, ValidUntil: validUntil,
		CommandSequence: 1,
		Targets: []*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1",
		}},
	}, events.PublishOptions{EventID: id, OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "logical-1"})
}

func (p runtimeEventPublisher) PublishMessage(_ context.Context, message *eventpb.EventMessage) (*jetstream.PublishAck, error) {
	if p.client == nil || !p.client.ready.Load() {
		return nil, errors.New("disconnected")
	}
	p.client.ids <- message.GetEventId()
	return &jetstream.PublishAck{}, nil
}

func TestRuntimeReconnectsAndCatchesUpPendingOutbox(t *testing.T) {
	data, err := strategyEventData("run-1")
	if err != nil {
		t.Fatal(err)
	}
	store := &runtimeTestStore{row: domain.OutboxMessage{MessageID: "run-1", EventData: data, CreatedAt: time.Now().Add(-time.Minute)}}
	client := newRuntimeTestClient()
	var attempts atomic.Int32
	runtime, err := NewRuntime(RuntimeConfig{
		Store: store, InstanceID: "strategy-test", RelayInterval: 5 * time.Millisecond,
		ReconnectInterval: 5 * time.Millisecond, BatchSize: 10,
		Probe: func(context.Context, JetStreamClient) error { return nil },
		Connector: func(context.Context) (JetStreamClient, error) {
			if attempts.Add(1) < 3 {
				return nil, errors.New("broker unavailable")
			}
			return client, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	eventually(t, time.Second, func() bool { return runtime.Connected() && store.isPublished() })
	select {
	case id := <-client.ids:
		if id != "run-1" {
			t.Fatalf("message id=%q", id)
		}
	default:
		t.Fatal("pending row was not published")
	}
}

func TestRuntimeDetectsBrokerLossAndReconnects(t *testing.T) {
	store := &runtimeTestStore{}
	first := newRuntimeTestClient()
	second := newRuntimeTestClient()
	var attempts atomic.Int32
	runtime, err := NewRuntime(RuntimeConfig{
		Store: store, RelayInterval: 5 * time.Millisecond, ReconnectInterval: 5 * time.Millisecond, BatchSize: 1,
		Probe: func(context.Context, JetStreamClient) error { return nil },
		Connector: func(context.Context) (JetStreamClient, error) {
			if attempts.Add(1) == 1 {
				return first, nil
			}
			return second, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, runtime.Connected)
	first.ready.Store(false)
	eventually(t, time.Second, func() bool { return attempts.Load() >= 2 && runtime.Connected() })
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if second.Ready() {
		t.Fatal("runtime Close left client connected")
	}
}

func TestRuntimeCloseBeforeStartRejectsLateStart(t *testing.T) {
	runtime, err := NewRuntime(RuntimeConfig{
		Store: &runtimeTestStore{}, RelayInterval: time.Millisecond, ReconnectInterval: time.Millisecond, BatchSize: 1,
		Connector: func(context.Context) (JetStreamClient, error) { return newRuntimeTestClient(), nil },
		Probe:     func(context.Context, JetStreamClient) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err == nil {
		t.Fatal("closed runtime restarted")
	}
}

func TestRuntimeDoesNotReportConnectedUntilPublishProbeSucceeds(t *testing.T) {
	probeAllowed := atomic.Bool{}
	runtime, err := NewRuntime(RuntimeConfig{
		Store: &runtimeTestStore{}, RelayInterval: time.Millisecond, ReconnectInterval: time.Millisecond, BatchSize: 1,
		Connector: func(context.Context) (JetStreamClient, error) { return newRuntimeTestClient(), nil },
		Probe: func(context.Context, JetStreamClient) error {
			if !probeAllowed.Load() {
				return errors.New("stream or ACL unavailable")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	time.Sleep(10 * time.Millisecond)
	if runtime.Connected() {
		t.Fatal("runtime reported ready before publish probe")
	}
	probeAllowed.Store(true)
	eventually(t, time.Second, runtime.Connected)
}

func eventually(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}
