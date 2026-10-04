package eventconsumer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDatasetQueueKeyUsesGovernedSubjectForMalformedPayload(t *testing.T) {
	space, err := jetstream.EncodeSubjectToken("space")
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := jetstream.EncodeSubjectToken("bars")
	if err != nil {
		t.Fatal(err)
	}
	rowSubject := "moox.event.storage.dataset.rows.upserted.v2." + space + "." + dataset
	markerSubject := "moox.event.storage.collector.period.completed.v1." + space + "." + dataset
	row, err := datasetQueueKey(nil, &jetstream.Delivery{Subject: rowSubject, DecodeError: errors.New("bad row")})
	if err != nil {
		t.Fatal(err)
	}
	marker, err := datasetQueueKey(nil, &jetstream.Delivery{Subject: markerSubject, DecodeError: errors.New("bad marker")})
	if err != nil {
		t.Fatal(err)
	}
	if row != "space\x00bars" || marker != row {
		t.Fatalf("queue keys row=%q marker=%q", row, marker)
	}
}

type dispatcherFactorHandler struct {
	datasetRowsHandlerFunc
	factor func()
}

func (h dispatcherFactorHandler) HandleFactorPeriodComputed(context.Context, *eventpb.EventMessage, *storagepb.FactorPeriodComputed) error {
	h.factor()
	return nil
}

func TestDatasetDispatcherFailedRowBlocksFactorUntilRedelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rowStarted, releaseRow := make(chan struct{}), make(chan struct{})
	markerDone, otherDone := make(chan struct{}), make(chan struct{})
	failed := make(chan error, 1)
	var attempts atomic.Int32
	var applied atomic.Bool
	consumer := testConsumer(t, dispatcherFactorHandler{
		datasetRowsHandlerFunc: func(context.Context, *eventpb.EventMessage, *storagepb.DatasetRowsUpserted) error {
			if attempts.Add(1) == 1 {
				close(rowStarted)
				select {
				case <-releaseRow:
				case <-ctx.Done():
					return ctx.Err()
				}
				return errors.New("row apply failed")
			}
			applied.Store(true)
			return nil
		},
		factor: func() { close(markerDone) },
	})
	rowEncoded, rowRaw := validDatasetDelivery(t)
	row := &jetstream.Delivery{Subject: rowEncoded.Subject, RawData: rowRaw, RawMessageID: rowEncoded.Message.GetEventId(), ContentType: events.ContentType, StreamSeq: 10, DeliveryCount: 1}
	at := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	markerEncoded, err := consumer.registry.Encode(events.FactorPeriodComputed, &storagepb.FactorPeriodComputed{
		DatasetId: "bar", SourceDatasetId: "prices", Frequency: "1m", PeriodTime: at.Unix(), Status: "complete",
		UniverseSubjectIds: []string{"ETH"}, Factors: []*storagepb.FactorPeriodState{{FactorId: "factor", Status: "complete", SourceHash: "hash"}},
		TriggerEventId: "source-ready", ComputedAt: timestamppb.New(at),
	}, events.PublishOptions{EventID: "factor-marker", OccurredAt: at, SpaceID: "foo", SubjectID: "bar"})
	if err != nil {
		t.Fatal(err)
	}
	markerRaw, err := proto.Marshal(markerEncoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	marker := &jetstream.Delivery{Subject: markerEncoded.Subject, RawData: markerRaw, RawMessageID: "factor-marker", ContentType: events.ContentType, StreamSeq: 11}
	d := newSubjectDispatcherWithKey(ctx, 2, 128, func(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat) error {
		if delivery.Subject == "other-dataset" {
			close(otherDone)
			return nil
		}
		return consumer.processDeliveryWithApplyAndActions(ctx, delivery, heartbeat, -1, consumer.applyDelivery, deliveryActions{
			ack:      func(context.Context) error { return nil },
			progress: func(context.Context) error { return nats.ErrConnectionClosed },
			term:     func(context.Context) error { t.Error("unexpected TERM"); return nil },
		})
	}, jetstream.ErrorReporterFunc(func(err error) { failed <- err }), func(delivery *jetstream.Delivery) (string, error) {
		if delivery.Subject == "other-dataset" {
			return delivery.Subject, nil
		}
		return datasetQueueKey(consumer.registry, delivery)
	})
	defer d.Close()
	if err := d.Dispatch(row); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rowStarted:
	case <-time.After(time.Second):
		t.Fatal("row did not start")
	}
	if err := d.Dispatch(marker); err != nil {
		t.Fatal(err)
	}
	if err := d.Dispatch(&jetstream.Delivery{Subject: "other-dataset", StreamSeq: 12}); err != nil {
		t.Fatal(err)
	}
	close(releaseRow)
	select {
	case err := <-failed:
		if !errors.Is(err, nats.ErrConnectionClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale row did not return")
	}
	select {
	case <-otherDone:
	case <-time.After(time.Second):
		t.Fatal("other Dataset lane is blocked")
	}
	select {
	case <-markerDone:
		t.Fatal("factor marker overtook unapplied row after stale transport")
	case <-time.After(50 * time.Millisecond):
	}
	retry := *row
	retry.DeliveryCount = 2
	if err := d.Dispatch(&retry); err != nil {
		t.Fatal(err)
	}
	select {
	case <-markerDone:
	case <-time.After(time.Second):
		t.Fatal("factor marker did not resume after row redelivery")
	}
	if !applied.Load() || attempts.Load() != 2 {
		t.Fatalf("row applied=%v attempts=%d", applied.Load(), attempts.Load())
	}
}

func TestDatasetDispatcherSuccessfulApplyWithStaleAckDoesNotBlockMarker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consumer := &Consumer{}
	markerDone := make(chan struct{})
	var applies atomic.Int32
	d := newSubjectDispatcher(ctx, 1, 8, func(ctx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat) error {
		if delivery.RawMessageID == "marker" {
			close(markerDone)
			return nil
		}
		return consumer.processDeliveryWithApplyAndActions(ctx, delivery, heartbeat, -1,
			func(context.Context, *jetstream.Delivery) error { applies.Add(1); return nil },
			deliveryActions{
				ack:      func(context.Context) error { return nats.ErrConnectionClosed },
				progress: func(context.Context) error { return nil },
				term:     func(context.Context) error { t.Error("unexpected TERM"); return nil },
			})
	}, nil)
	defer d.Close()
	if err := d.Dispatch(&jetstream.Delivery{Subject: "dataset", RawMessageID: "row", StreamSeq: 20}); err != nil {
		t.Fatal(err)
	}
	if err := d.Dispatch(&jetstream.Delivery{Subject: "dataset", RawMessageID: "marker", StreamSeq: 21}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-markerDone:
	case <-time.After(time.Second):
		t.Fatal("successful row apply was blocked on uncertain ACK")
	}
	if applies.Load() != 1 {
		t.Fatalf("successful apply repeated %d times", applies.Load())
	}
}

func TestDatasetDispatcherFailedBatchWaitsForEveryUnsettledSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	batchFailed := make(chan error, 1)
	markerDone := make(chan struct{})
	rowApplied := make(chan uint64, 2)
	var mu sync.Mutex
	var order []uint64
	d := newSubjectDispatcherWithKeyAndBatch(ctx, 1, 8, func(_ context.Context, delivery *jetstream.Delivery, _ *deliveryHeartbeat) error {
		if delivery.RawMessageID == "marker" {
			close(markerDone)
			return nil
		}
		mu.Lock()
		order = append(order, delivery.StreamSeq)
		mu.Unlock()
		rowApplied <- delivery.StreamSeq
		return nil
	}, func(context.Context, []*jetstream.Delivery, []*deliveryHeartbeat) error {
		return nats.ErrConnectionClosed
	}, 8, func(delivery *jetstream.Delivery) bool { return delivery.RawMessageID != "marker" }, jetstream.ErrorReporterFunc(func(err error) { batchFailed <- err }), nil)
	defer d.Close()
	// Hold the scheduler while placing both rows in the same batch.
	d.mu.Lock()
	queue := &subjectQueue{subject: "dataset", running: true, deliveries: []*queuedDelivery{
		{delivery: &jetstream.Delivery{Subject: "dataset", StreamSeq: 30}},
		{delivery: &jetstream.Delivery{Subject: "dataset", StreamSeq: 31}},
		{delivery: &jetstream.Delivery{Subject: "dataset", RawMessageID: "marker", StreamSeq: 32}},
	}}
	d.queues[queue.subject] = queue
	for range queue.deliveries {
		d.pending <- struct{}{}
	}
	d.mu.Unlock()
	d.ready <- queue
	select {
	case <-batchFailed:
	case <-time.After(time.Second):
		t.Fatal("batch did not fail")
	}
	// JetStream can redeliver a later failed sequence first; it must not
	// bypass the earliest unresolved row or the following marker.
	if err := d.Dispatch(&jetstream.Delivery{Subject: "dataset", StreamSeq: 31, DeliveryCount: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rowApplied:
		t.Fatal("later redelivery overtook earliest failed row")
	case <-time.After(50 * time.Millisecond):
	}
	if err := d.Dispatch(&jetstream.Delivery{Subject: "dataset", StreamSeq: 30, DeliveryCount: 2}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-markerDone:
	case <-time.After(time.Second):
		t.Fatal("marker did not resume after every batch row applied")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != 30 || order[1] != 31 {
		t.Fatalf("redelivery order=%v", order)
	}
}

func TestDeliveryHeartbeatIntervalUsesAckWait(t *testing.T) {
	if got := deliveryHeartbeatInterval(120 * time.Second); got != 30*time.Second {
		t.Fatalf("heartbeat interval = %s, want 30s", got)
	}
	if got := deliveryHeartbeatInterval(18 * time.Second); got != 6*time.Second {
		t.Fatalf("heartbeat interval = %s, want 6s", got)
	}
	if got := deliveryHeartbeatInterval(500 * time.Millisecond); got != time.Second {
		t.Fatalf("heartbeat lower bound = %s, want 1s", got)
	}
}

func TestSubjectDispatcherDifferentSubjectsRunInParallel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	aStarted, bStarted, releaseA := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d := newSubjectDispatcher(ctx, 2, 8, func(_ context.Context, delivery *jetstream.Delivery, _ *deliveryHeartbeat) error {
		if delivery.Subject == "A" {
			close(aStarted)
			<-releaseA
		} else {
			close(bStarted)
		}
		return nil
	}, nil)
	defer d.Close()
	if err := d.Dispatch(&jetstream.Delivery{Subject: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Dispatch(&jetstream.Delivery{Subject: "B"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-aStarted:
	case <-time.After(time.Second):
		t.Fatal("subject A did not start")
	}
	select {
	case <-bStarted:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("subject B was blocked by subject A")
	}
	close(releaseA)
}

func TestSubjectDispatcherPreservesSubjectOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstStarted, releaseFirst, secondDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []string
	d := newSubjectDispatcher(ctx, 4, 8, func(_ context.Context, delivery *jetstream.Delivery, _ *deliveryHeartbeat) error {
		if delivery.RawMessageID == "first" {
			close(firstStarted)
			<-releaseFirst
		}
		mu.Lock()
		order = append(order, delivery.RawMessageID)
		mu.Unlock()
		if delivery.RawMessageID == "second" {
			close(secondDone)
		}
		return nil
	}, nil)
	defer d.Close()
	if err := d.Dispatch(&jetstream.Delivery{Subject: "same", RawMessageID: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Dispatch(&jetstream.Delivery{Subject: "same", RawMessageID: "second"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not start")
	}
	select {
	case <-secondDone:
		t.Fatal("same subject overtook an active delivery")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second delivery did not run")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("order = %v", order)
	}
}

func TestDatasetQueueKeySerializesRowsAndMarkersForOneDataset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstStarted, releaseFirst, secondDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d := newSubjectDispatcherWithKey(ctx, 2, 8, func(_ context.Context, delivery *jetstream.Delivery, _ *deliveryHeartbeat) error {
		if delivery.RawMessageID == "row" {
			close(firstStarted)
			<-releaseFirst
		}
		if delivery.RawMessageID == "marker" {
			close(secondDone)
		}
		return nil
	}, nil, func(delivery *jetstream.Delivery) (string, error) {
		return "space/dataset", nil
	})
	defer d.Close()
	if err := d.Dispatch(&jetstream.Delivery{Subject: "rows.upserted", RawMessageID: "row"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Dispatch(&jetstream.Delivery{Subject: "period.collected", RawMessageID: "marker"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("row delivery did not start")
	}
	select {
	case <-secondDone:
		t.Fatal("marker overtook the dataset row")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("marker did not run after row completed")
	}
}

func TestSubjectDispatcherNextBatchStopsBeforeMarker(t *testing.T) {
	d := &subjectDispatcher{
		batch:         func(context.Context, []*jetstream.Delivery, []*deliveryHeartbeat) error { return nil },
		batchSize:     8,
		batchEligible: func(delivery *jetstream.Delivery) bool { return delivery != nil && delivery.Subject == "rows" },
		queues:        make(map[string]*subjectQueue),
	}
	queue := &subjectQueue{subject: "space/dataset", deliveries: []*queuedDelivery{
		{delivery: &jetstream.Delivery{Subject: "rows", RawMessageID: "row-1"}},
		{delivery: &jetstream.Delivery{Subject: "rows", RawMessageID: "row-2"}},
		{delivery: &jetstream.Delivery{Subject: "marker", RawMessageID: "marker-1"}},
		{delivery: &jetstream.Delivery{Subject: "rows", RawMessageID: "row-3"}},
	}}
	first, ok := d.nextBatch(queue)
	if !ok || len(first) != 2 {
		t.Fatalf("first batch length = %d, ok=%v; want 2", len(first), ok)
	}
	if got := first[0].delivery.RawMessageID + "," + first[1].delivery.RawMessageID; got != "row-1,row-2" {
		t.Fatalf("first batch = %s", got)
	}
	second, ok := d.nextBatch(queue)
	if !ok || len(second) != 1 || second[0].delivery.RawMessageID != "marker-1" {
		t.Fatalf("second item = %#v, ok=%v; want marker", second, ok)
	}
}

func TestSubjectDispatcherBackpressuresAtMaxPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	d := newSubjectDispatcher(ctx, 1, 1, func(_ context.Context, delivery *jetstream.Delivery, _ *deliveryHeartbeat) error {
		if delivery.RawMessageID == "first" {
			close(firstStarted)
			<-releaseFirst
		}
		return nil
	}, nil)
	defer d.Close()
	if err := d.Dispatch(&jetstream.Delivery{Subject: "same", RawMessageID: "first"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not start")
	}
	secondReturned := make(chan error, 1)
	go func() {
		secondReturned <- d.Dispatch(&jetstream.Delivery{Subject: "same", RawMessageID: "second"})
	}()
	select {
	case err := <-secondReturned:
		t.Fatalf("second dispatch bypassed max_pending: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case err := <-secondReturned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second dispatch did not resume after capacity was released")
	}
}

func TestSubjectDispatcherRetryKeepsSubjectBlockedButOtherSubjectRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	aFailed, retryA, aSecond, bStarted := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	d := newSubjectDispatcher(ctx, 2, 8, func(_ context.Context, delivery *jetstream.Delivery, _ *deliveryHeartbeat) error {
		switch delivery.RawMessageID {
		case "a1":
			close(aFailed)
			<-retryA // processDelivery holds the pending delivery during retry.
		case "a2":
			close(aSecond)
		case "b1":
			close(bStarted)
		}
		return nil
	}, nil)
	defer d.Close()
	for _, delivery := range []*jetstream.Delivery{{Subject: "A", RawMessageID: "a1"}, {Subject: "A", RawMessageID: "a2"}, {Subject: "B", RawMessageID: "b1"}} {
		if err := d.Dispatch(delivery); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-aFailed:
	case <-time.After(time.Second):
		t.Fatal("failed delivery did not start")
	}
	select {
	case <-bStarted:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("other subject was blocked by retry")
	}
	select {
	case <-aSecond:
		t.Fatal("same subject overtook retry")
	case <-time.After(50 * time.Millisecond):
	}
	close(retryA)
	select {
	case <-aSecond:
	case <-time.After(time.Second):
		t.Fatal("same subject did not resume")
	}
}

func TestSubjectDispatcherStartsHeartbeatWhenDeliveryIsQueuedAndStopsItOnClose(t *testing.T) {
	ctx := context.Background()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	queuedHeartbeat := make(chan *deliveryHeartbeat, 2)
	d := newSubjectDispatcher(ctx, 1, 8, func(handlerCtx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat) error {
		if delivery.RawMessageID == "first" {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-handlerCtx.Done():
			}
		}
		if delivery.RawMessageID == "second" {
			t.Fatalf("queued delivery started before first finished")
		}
		if heartbeat == nil {
			t.Fatal("handler did not receive queued heartbeat")
		}
		return nil
	}, nil, subjectDispatcherMetricsHooks{
		newHeartbeat: func(context.Context, *jetstream.Delivery) *deliveryHeartbeat {
			h := &deliveryHeartbeat{stopCh: make(chan struct{}), doneCh: make(chan struct{})}
			go func() {
				<-h.stopCh
				close(h.doneCh)
			}()
			queuedHeartbeat <- h
			return h
		},
	})
	if err := d.Dispatch(&jetstream.Delivery{Subject: "same", RawMessageID: "first"}); err != nil {
		t.Fatal(err)
	}
	<-queuedHeartbeat // the first delivery also starts its heartbeat at dispatch.
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not start")
	}
	if err := d.Dispatch(&jetstream.Delivery{Subject: "same", RawMessageID: "second"}); err != nil {
		t.Fatal(err)
	}
	var heartbeat *deliveryHeartbeat
	select {
	case heartbeat = <-queuedHeartbeat:
	case <-time.After(time.Second):
		t.Fatal("queued delivery did not start its heartbeat")
	}
	d.Close()
	select {
	case <-heartbeat.doneCh:
	case <-time.After(time.Second):
		t.Fatal("queued heartbeat was not stopped on dispatcher close")
	}
	close(releaseFirst)
}
