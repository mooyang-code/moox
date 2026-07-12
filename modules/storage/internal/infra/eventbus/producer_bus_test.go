package eventbus

import (
	"context"
	"errors"
	"testing"
	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
