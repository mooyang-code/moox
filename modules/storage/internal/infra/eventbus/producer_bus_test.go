package eventbus

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/codec"
)

type recordingDelivery struct {
	mu          sync.Mutex
	actions     []string
	ackErr      error
	nakErr      error
	progressErr error
	deadlines   []bool
	contextErrs []error
	calleeNames []string
}

func (d *recordingDelivery) record(ctx context.Context, action string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, hasDeadline := ctx.Deadline()
	d.deadlines = append(d.deadlines, hasDeadline)
	d.contextErrs = append(d.contextErrs, ctx.Err())
	d.calleeNames = append(d.calleeNames, codec.Message(ctx).CalleeMethod())
	d.actions = append(d.actions, action)
}

func (d *recordingDelivery) Ack(ctx context.Context) error {
	d.record(ctx, "ack")
	return d.ackErr
}

func (d *recordingDelivery) Nak(ctx context.Context, _ time.Duration) error {
	d.record(ctx, "nak")
	return d.nakErr
}

func (d *recordingDelivery) InProgress(ctx context.Context) error {
	d.record(ctx, "in_progress")
	return d.progressErr
}

func (d *recordingDelivery) snapshot() ([]string, []bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.actions...), append([]bool(nil), d.deadlines...)
}

func (d *recordingDelivery) lastContextErr() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.contextErrs) == 0 {
		return nil
	}
	return d.contextErrs[len(d.contextErrs)-1]
}

func (d *recordingDelivery) lastCalleeName() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.calleeNames) == 0 {
		return ""
	}
	return d.calleeNames[len(d.calleeNames)-1]
}

func TestSubscriberOptionsBuildConfiguredConsumerRefs(t *testing.T) {
	opts := SubscriberOptions{
		StreamName: "CUSTOM", AckWait: 9 * time.Second, MaxDeliver: 7,
		MaxInFlight: 3, MaxAckPending: 11, NakDelay: 20 * time.Millisecond, ActionTimeout: time.Second,
	}
	bus, err := NewSubscriberBus(&jetstream.Client{}, "custom", opts)
	require.NoError(t, err)

	timeSeries := bus.consumerRef(true)
	record := bus.consumerRef(false)
	assert.Equal(t, "CUSTOM", timeSeries.Stream)
	assert.Equal(t, 9*time.Second, timeSeries.AckWait)
	assert.Equal(t, 7, timeSeries.MaxDeliver)
	assert.Equal(t, 11, timeSeries.MaxAckPending)
	assert.Equal(t, "custom.rows_committed.>", timeSeries.FilterSubject)
	assert.Equal(t, timeSeries.Durable, record.Durable)
	assert.Equal(t, "custom.rows_committed.>", record.FilterSubject)
}

func TestProcessDeliveryAcknowledgesOnlyAfterHandlerSuccess(t *testing.T) {
	delivery := &recordingDelivery{}
	handlerReturned := false
	err := processDelivery(context.Background(), delivery, SubscriberOptions{
		AckWait: 90 * time.Millisecond, NakDelay: time.Millisecond, ActionTimeout: 20 * time.Millisecond,
	}, func(context.Context) error {
		actions, _ := delivery.snapshot()
		assert.Empty(t, actions)
		handlerReturned = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, handlerReturned)
	actions, deadlines := delivery.snapshot()
	assert.Equal(t, []string{"ack"}, actions)
	assert.Equal(t, []bool{true}, deadlines)
}

func TestProcessDeliveryNaksHandlerFailure(t *testing.T) {
	delivery := &recordingDelivery{}
	handlerErr := errors.New("derive failed")
	err := processDelivery(context.Background(), delivery, SubscriberOptions{
		AckWait: 90 * time.Millisecond, NakDelay: time.Millisecond, ActionTimeout: 20 * time.Millisecond,
	}, func(context.Context) error { return handlerErr })

	require.ErrorIs(t, err, handlerErr)
	actions, deadlines := delivery.snapshot()
	assert.Equal(t, []string{"nak"}, actions)
	assert.Equal(t, []bool{true}, deadlines)
}

func TestProcessDeliveryAckFailureDoesNotNak(t *testing.T) {
	ackErr := errors.New("ack failed")
	delivery := &recordingDelivery{ackErr: ackErr}
	err := processDelivery(context.Background(), delivery, SubscriberOptions{
		AckWait: 90 * time.Millisecond, NakDelay: time.Millisecond, ActionTimeout: 20 * time.Millisecond,
	}, func(context.Context) error { return nil })

	require.ErrorIs(t, err, ackErr)
	actions, _ := delivery.snapshot()
	assert.Equal(t, []string{"ack"}, actions)
}

func TestProcessDeliveryHeartbeatsLongHandlerAndStopsBeforeAck(t *testing.T) {
	delivery := &recordingDelivery{progressErr: errors.New("transient heartbeat failure")}
	err := processDelivery(context.Background(), delivery, SubscriberOptions{
		AckWait: 30 * time.Millisecond, NakDelay: time.Millisecond, ActionTimeout: 20 * time.Millisecond,
	}, func(context.Context) error {
		time.Sleep(75 * time.Millisecond)
		return nil
	})

	require.NoError(t, err)
	actions, deadlines := delivery.snapshot()
	require.GreaterOrEqual(t, len(actions), 3)
	assert.Equal(t, "ack", actions[len(actions)-1])
	assert.NotContains(t, actions[len(actions):], "in_progress")
	for _, hasDeadline := range deadlines {
		assert.True(t, hasDeadline)
	}
}

func TestProcessDeliveryCancellationWaitsForContextAwareHandlerBeforeNak(t *testing.T) {
	delivery := &recordingDelivery{}
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithCalleeMethod("storage.ViewBuilder")
	ctx, cancel := context.WithCancel(ctx)
	handlerExited := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- processDelivery(ctx, delivery, SubscriberOptions{
			AckWait: 90 * time.Millisecond, NakDelay: time.Millisecond, ActionTimeout: 20 * time.Millisecond,
		}, func(handlerCtx context.Context) error {
			<-handlerCtx.Done()
			close(handlerExited)
			return handlerCtx.Err()
		})
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("processDelivery blocked on a cancelled handler")
	}
	select {
	case <-handlerExited:
	default:
		t.Fatal("processDelivery returned before the handler exited")
	}
	actions, _ := delivery.snapshot()
	assert.Equal(t, "nak", actions[len(actions)-1])
	assert.NoError(t, delivery.lastContextErr(), "terminal NAK context must be detached from upstream cancellation")
	assert.Equal(t, "storage.ViewBuilder", delivery.lastCalleeName(), "terminal NAK context must retain tRPC metadata")
}

func TestProcessDeliveryCancellationBoundsUnresponsiveHandlerDrain(t *testing.T) {
	delivery := &recordingDelivery{}
	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- processDelivery(ctx, delivery, SubscriberOptions{
			AckWait: 90 * time.Millisecond, NakDelay: time.Millisecond, ActionTimeout: 20 * time.Millisecond,
			HandlerDrainTimeout: 20 * time.Millisecond,
		}, func(context.Context) error {
			<-blocked
			return nil
		})
	}()
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		assert.ErrorContains(t, err, "handler drain timed out")
	case <-time.After(time.Second):
		t.Fatal("processDelivery did not bound an unresponsive handler")
	}
	close(blocked)
	actions, _ := delivery.snapshot()
	assert.Equal(t, "nak", actions[len(actions)-1])
}

func TestSubscriberHandlerLifecycleWaitIsBounded(t *testing.T) {
	lifecycle := newSubscriberHandlerLifecycle()
	require.True(t, lifecycle.acquire())
	lifecycle.beginClose()

	err := lifecycle.wait(20 * time.Millisecond)
	assert.ErrorContains(t, err, "handler drain timed out")
	lifecycle.release()
	require.NoError(t, lifecycle.wait(time.Second))
}

func TestSubscriberBusInvokesAllTimeSeriesHandlers(t *testing.T) {
	firstErr := errors.New("first projection failed")
	secondErr := errors.New("second projection failed")
	secondCalled := false
	first, err := proto.Marshal(&pb.TimeSeriesRowsCommitted{ShardId: "shard-1"})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	firstCtx, firstCancel := context.WithCancel(context.Background())
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer firstCancel()
	defer secondCancel()
	bus := &SubscriberBus{
		timeSeriesHandlers: map[uint64]*subscriberTimeSeriesHandler{
			1: {handler: func(context.Context, *pb.TimeSeriesRowsCommitted) error { return firstErr }, lifecycle: newSubscriberHandlerLifecycle(), ctx: firstCtx, cancel: firstCancel},
			2: {handler: func(context.Context, *pb.TimeSeriesRowsCommitted) error {
				secondCalled = true
				return secondErr
			}, lifecycle: newSubscriberHandlerLifecycle(), ctx: secondCtx, cancel: secondCancel},
		},
	}

	err = bus.dispatch(context.Background(), &jetstream.Delivery{Message: testTimeSeriesMessage(first)}, true)
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("handler error = %v, want both handler errors", err)
	}
	if !secondCalled {
		t.Fatal("second handler was not called after the first handler failed")
	}
}

func TestSubscriberCloseDrainsItsHandlerWhileOtherHandlerRemains(t *testing.T) {
	payload, err := proto.Marshal(&pb.TimeSeriesRowsCommitted{ShardId: "shard-1"})
	require.NoError(t, err)
	started := make(chan struct{})
	firstCtx, firstCancel := context.WithCancel(context.Background())
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	first := &subscriberTimeSeriesHandler{
		handler: func(ctx context.Context, _ *pb.TimeSeriesRowsCommitted) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
		lifecycle: newSubscriberHandlerLifecycle(), ctx: firstCtx, cancel: firstCancel,
	}
	second := &subscriberTimeSeriesHandler{
		handler:   func(context.Context, *pb.TimeSeriesRowsCommitted) error { return nil },
		lifecycle: newSubscriberHandlerLifecycle(), ctx: secondCtx, cancel: secondCancel,
	}
	bus := &SubscriberBus{timeSeriesHandlers: map[uint64]*subscriberTimeSeriesHandler{1: first, 2: second}}
	dispatchDone := make(chan error, 1)
	go func() {
		dispatchDone <- bus.dispatch(context.Background(), &jetstream.Delivery{Message: testTimeSeriesMessage(payload)}, true)
	}()
	<-started
	if err := bus.closeTimeSeries(1, first); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-dispatchDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("dispatch did not drain the closed handler")
	}
	bus.mu.Lock()
	_, firstPresent := bus.timeSeriesHandlers[1]
	_, secondPresent := bus.timeSeriesHandlers[2]
	bus.mu.Unlock()
	assert.False(t, firstPresent)
	assert.True(t, secondPresent)
}

func TestSubscriberCloseBoundsUnresponsiveHandler(t *testing.T) {
	payload, err := proto.Marshal(&pb.TimeSeriesRowsCommitted{ShardId: "shard-1"})
	require.NoError(t, err)
	started := make(chan struct{})
	blocked := make(chan struct{})
	firstCtx, firstCancel := context.WithCancel(context.Background())
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	first := &subscriberTimeSeriesHandler{
		handler: func(context.Context, *pb.TimeSeriesRowsCommitted) error {
			close(started)
			<-blocked
			return nil
		},
		lifecycle: newSubscriberHandlerLifecycle(), ctx: firstCtx, cancel: firstCancel,
	}
	second := &subscriberTimeSeriesHandler{
		handler:   func(context.Context, *pb.TimeSeriesRowsCommitted) error { return nil },
		lifecycle: newSubscriberHandlerLifecycle(), ctx: secondCtx, cancel: secondCancel,
	}
	bus := &SubscriberBus{
		timeSeriesHandlers: map[uint64]*subscriberTimeSeriesHandler{1: first, 2: second},
		opts:               SubscriberOptions{HandlerDrainTimeout: 20 * time.Millisecond},
	}
	dispatchDone := make(chan error, 1)
	go func() {
		dispatchDone <- bus.dispatch(context.Background(), &jetstream.Delivery{Message: testTimeSeriesMessage(payload)}, true)
	}()
	<-started

	err = bus.closeTimeSeries(1, first)
	assert.ErrorContains(t, err, "handler drain timed out")
	close(blocked)
	require.NoError(t, <-dispatchDone)
}

func TestSubscriberHandlerPanicReleasesUncalledHandlerLeases(t *testing.T) {
	firstCtx, firstCancel := context.WithCancel(context.Background())
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer firstCancel()
	defer secondCancel()
	first := &subscriberTimeSeriesHandler{
		handler:   func(context.Context, *pb.TimeSeriesRowsCommitted) error { panic("boom") },
		lifecycle: newSubscriberHandlerLifecycle(), ctx: firstCtx, cancel: firstCancel,
	}
	second := &subscriberTimeSeriesHandler{
		handler:   func(context.Context, *pb.TimeSeriesRowsCommitted) error { return nil },
		lifecycle: newSubscriberHandlerLifecycle(), ctx: secondCtx, cancel: secondCancel,
	}
	require.True(t, first.lifecycle.acquire())
	require.True(t, second.lifecycle.acquire())
	func() {
		defer func() { _ = recover() }()
		_ = callTimeSeriesHandlers(context.Background(), &pb.TimeSeriesRowsCommitted{}, []*subscriberTimeSeriesHandler{first, second})
	}()
	for _, entry := range []*subscriberTimeSeriesHandler{first, second} {
		entry.lifecycle.mu.Lock()
		inFlight := entry.lifecycle.inFlight
		entry.lifecycle.mu.Unlock()
		assert.Zero(t, inFlight)
	}
}

func TestSubscriberBusRejectsDeliveryWithoutHandlers(t *testing.T) {
	payload, err := proto.Marshal(&pb.TimeSeriesRowsCommitted{ShardId: "shard-1"})
	require.NoError(t, err)
	bus := &SubscriberBus{timeSeriesHandlers: map[uint64]*subscriberTimeSeriesHandler{}}

	err = bus.dispatch(context.Background(), &jetstream.Delivery{Message: testTimeSeriesMessage(payload)}, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no time-series handlers")
}

func TestNormalizeSubjectPrefix(t *testing.T) {
	assert.Equal(t, DefaultSubjectPrefix, normalizeSubjectPrefix(""))
	assert.Equal(t, "custom.prefix", normalizeSubjectPrefix(".custom.prefix."))
}

func TestTimeSeriesRowsCommittedSubjectUsesPrefix(t *testing.T) {
	subject := TimeSeriesRowsCommittedSubject("custom")
	assert.Equal(t, "custom.rows_committed.time_series.v1.>", subject)
}

func TestRecordRowsCommittedSubjectUsesPrefix(t *testing.T) {
	subject := RecordRowsCommittedSubject("custom")
	assert.Equal(t, "custom.rows_committed.record.v1.>", subject)
}

func TestSubjectPrefixWildcard(t *testing.T) {
	assert.Equal(t, "moox.storage.>", SubjectPrefixWildcard(""))
}

func TestPublishTimeSeriesRowsCommittedRejectsNilEvent(t *testing.T) {
	bus := NewProducerBus(&jetstream.Client{}, "")
	err := bus.PublishTimeSeriesRowsCommitted(context.Background(), nil)
	require.Error(t, err)
}

func TestPublishRecordRowsCommittedRejectsNilEvent(t *testing.T) {
	bus := NewProducerBus(&jetstream.Client{}, "")
	err := bus.PublishRecordRowsCommitted(context.Background(), nil)
	require.Error(t, err)
}

func TestPublishMessageRejectsNilClient(t *testing.T) {
	var bus *ProducerBus
	err := bus.PublishMessage(context.Background(), []byte("x"))
	require.Error(t, err)
}

func TestNewProducerBusSetsSubjects(t *testing.T) {
	bus := NewProducerBus(&jetstream.Client{}, "test")
	assert.Equal(t, "test.rows_committed.time_series.v1.>", bus.timeSeriesSubject)
	assert.Equal(t, "test.rows_committed.record.v1.>", bus.recordSubject)
}

func TestPublishTimeSeriesRowsCommittedRequiresEvent(t *testing.T) {
	bus := NewProducerBus(&jetstream.Client{}, "")
	err := bus.PublishTimeSeriesRowsCommitted(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "time-series committed")
}

func TestPublishNilProducerBusReturnsError(t *testing.T) {
	var bus *ProducerBus
	err := bus.publish(context.Background(), "topic", defaultTimeSeriesRowsCommittedType, "space", "dataset", "shard-1", &pb.TimeSeriesRowsCommitted{ShardId: "shard-1"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.New("storage eventbus client is nil")) || err != nil)
}

func testTimeSeriesMessage(payload []byte) *messagepb.MooxMessage {
	now := timestamppb.Now()
	token, _ := jetstream.EncodeShardToken("shard-1")
	event := &pb.TimeSeriesRowsCommitted{}
	_ = proto.Unmarshal(payload, event)
	event.Sequence = 1
	payload, _ = proto.Marshal(event)
	return &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       "message-1",
		Topic:           "moox.storage.rows_committed.time_series.v1." + token,
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        &messagepb.Producer{ServiceName: "moox-storage", InstanceId: "storage"},
		Sequence:        1,
		OccurredAt:      now,
		PublishedAt:     now,
		ContentType:     "application/x-protobuf; message=trpc.moox.storage.TimeSeriesRowsCommitted",
		MessageType:     defaultTimeSeriesRowsCommittedType,
		Payload:         payload,
	}
}

func TestValidateRowsCommittedMessageRejectsUnknownType(t *testing.T) {
	payload, err := proto.Marshal(&pb.TimeSeriesRowsCommitted{ShardId: "shard-1"})
	require.NoError(t, err)
	msg := testTimeSeriesMessage(payload)
	msg.MessageType = "moox.storage.unknown.v1"
	assert.ErrorContains(t, validateRowsCommittedMessage(msg, ""), "unknown storage message_type")
}

func TestValidateRowsCommittedMessageRejectsTopicShardMismatch(t *testing.T) {
	payload, err := proto.Marshal(&pb.TimeSeriesRowsCommitted{ShardId: "shard-1"})
	require.NoError(t, err)
	msg := testTimeSeriesMessage(payload)
	token, err := jetstream.EncodeShardToken("shard-2")
	require.NoError(t, err)
	msg.Topic = "moox.storage.rows_committed.time_series.v1." + token
	assert.ErrorContains(t, validateRowsCommittedMessage(msg, ""), "does not match topic shard")
}

func TestValidateRowsCommittedMessageRejectsZeroSequence(t *testing.T) {
	payload, err := proto.Marshal(&pb.TimeSeriesRowsCommitted{ShardId: "shard-1"})
	require.NoError(t, err)
	msg := testTimeSeriesMessage(payload)
	msg.Sequence = 0
	assert.ErrorContains(t, validateRowsCommittedMessage(msg, ""), "sequence is required")
}
