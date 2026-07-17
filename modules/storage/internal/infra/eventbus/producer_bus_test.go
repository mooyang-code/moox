package eventbus

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type recordingDelivery struct {
	mu          sync.Mutex
	actions     []string
	ackErr      error
	nakErr      error
	progressErr error
	deadlines   []bool
}

func (d *recordingDelivery) record(ctx context.Context, action string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, hasDeadline := ctx.Deadline()
	d.deadlines = append(d.deadlines, hasDeadline)
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
	assert.Equal(t, "custom.time_series.rows_updated.v1", timeSeries.FilterSubject)
	assert.Equal(t, "custom.record.rows_updated.v1", record.FilterSubject)
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

func TestProcessDeliveryCancellationNaksWithoutWaitingForStuckHandler(t *testing.T) {
	delivery := &recordingDelivery{}
	handlerRelease := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- processDelivery(ctx, delivery, SubscriberOptions{
			AckWait: 90 * time.Millisecond, NakDelay: time.Millisecond, ActionTimeout: 20 * time.Millisecond,
		}, func(context.Context) error {
			<-handlerRelease
			return nil
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
	close(handlerRelease)
	actions, _ := delivery.snapshot()
	assert.Equal(t, "nak", actions[len(actions)-1])
}

func TestSubscriberBusInvokesAllTimeSeriesHandlers(t *testing.T) {
	firstErr := errors.New("first projection failed")
	secondErr := errors.New("second projection failed")
	secondCalled := false
	first, err := proto.Marshal(&pb.TimeSeriesRowsUpdated{MessageId: "event-1"})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	bus := &SubscriberBus{
		timeSeriesHandlers: map[uint64]coreeventbus.TimeSeriesRowsUpdatedHandler{
			1: func(context.Context, *pb.TimeSeriesRowsUpdated) error { return firstErr },
			2: func(context.Context, *pb.TimeSeriesRowsUpdated) error {
				secondCalled = true
				return secondErr
			},
		},
	}

	err = bus.dispatch(context.Background(), &jetstream.Delivery{Message: &messagepb.MooxMessage{Payload: first}}, true)
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("handler error = %v, want both handler errors", err)
	}
	if !secondCalled {
		t.Fatal("second handler was not called after the first handler failed")
	}
}

func TestSubscriberBusRejectsDeliveryWithoutHandlers(t *testing.T) {
	payload, err := proto.Marshal(&pb.TimeSeriesRowsUpdated{MessageId: "event-1"})
	require.NoError(t, err)
	bus := &SubscriberBus{timeSeriesHandlers: map[uint64]coreeventbus.TimeSeriesRowsUpdatedHandler{}}

	err = bus.dispatch(context.Background(), &jetstream.Delivery{Message: &messagepb.MooxMessage{Payload: payload}}, true)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no time-series handlers")
}

func TestNormalizeSubjectPrefix(t *testing.T) {
	assert.Equal(t, DefaultSubjectPrefix, normalizeSubjectPrefix(""))
	assert.Equal(t, "custom.prefix", normalizeSubjectPrefix(".custom.prefix."))
}

func TestTimeSeriesRowsUpdatedSubjectUsesPrefix(t *testing.T) {
	subject := TimeSeriesRowsUpdatedSubject("custom")
	assert.Equal(t, "custom.time_series.rows_updated.v1", subject)
}

func TestRecordRowsUpdatedSubjectUsesPrefix(t *testing.T) {
	subject := RecordRowsUpdatedSubject("custom")
	assert.Equal(t, "custom.record.rows_updated.v1", subject)
}

func TestSubjectPrefixWildcard(t *testing.T) {
	assert.Equal(t, "moox.storage.>", SubjectPrefixWildcard(""))
}

func TestPublishTimeSeriesRowsUpdatedRejectsNilEvent(t *testing.T) {
	bus := NewProducerBus(&jetstream.Client{}, "")
	err := bus.PublishTimeSeriesRowsUpdated(context.Background(), nil)
	require.Error(t, err)
}

func TestPublishRecordRowsUpdatedRejectsNilEvent(t *testing.T) {
	bus := NewProducerBus(&jetstream.Client{}, "")
	err := bus.PublishRecordRowsUpdated(context.Background(), nil)
	require.Error(t, err)
}

func TestPublishEnvelopeRejectsNilClient(t *testing.T) {
	var bus *ProducerBus
	err := bus.PublishEnvelope(context.Background(), []byte("x"))
	require.Error(t, err)
}

func TestPublishEnvelopesReturnsUnmarshalErrors(t *testing.T) {
	bus := NewProducerBus(&jetstream.Client{}, "")
	errs := bus.PublishEnvelopes(context.Background(), [][]byte{[]byte("bad")})
	require.Len(t, errs, 1)
	require.Error(t, errs[0])
}

func TestNewProducerBusSetsSubjects(t *testing.T) {
	bus := NewProducerBus(&jetstream.Client{}, "test")
	assert.Equal(t, "test.time_series.rows_updated.v1", bus.timeSeriesSubject)
	assert.Equal(t, "test.record.rows_updated.v1", bus.recordSubject)
}

func TestPublishTimeSeriesRowsUpdatedRequiresMessageID(t *testing.T) {
	bus := NewProducerBus(&jetstream.Client{}, "")
	err := bus.PublishTimeSeriesRowsUpdated(context.Background(), &pb.TimeSeriesRowsUpdated{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "message_id")
}

func TestPublishNilProducerBusReturnsError(t *testing.T) {
	var bus *ProducerBus
	err := bus.publish(context.Background(), "topic", "", "space", "dataset", &pb.TimeSeriesRowsUpdated{MessageId: "id-1"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.New("storage eventbus client is nil")) || err != nil)
}
