package eventconsumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type subjectSubmitFunc func(context.Context, trigger.SubjectEvent) error

func (f subjectSubmitFunc) Submit(ctx context.Context, event trigger.SubjectEvent) error {
	return f(ctx, event)
}

func TestSubjectHandlerPreservesOriginAndRetriesExecutionFailures(t *testing.T) {
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	now := time.Now()
	payload := &storagepb.ViewSourceSubjectReady{SourceViewId: "prices", SourceDatasetId: "bars", SubjectId: "BTC", Frequency: "1m", PeriodTime: now.Unix(), ActiveIndexId: "index", InputContractVersion: "schema:1", SourceEventId: "origin", SourceNodeId: "node", SourceStoreId: "store", SourceSequence: 123, ReadyAt: timestamppb.New(now)}
	encoded, err := registry.Encode(events.ViewSourceSubjectReady, payload, events.PublishOptions{EventID: "ready", OccurredAt: now, SpaceID: "space", SubjectID: "prices"})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	delivery := &jetstream.Delivery{Subject: encoded.Subject, RawData: raw, RawMessageID: "ready", ContentType: events.ContentType}
	for _, failure := range []error{nil, errors.New("write failed"), context.Canceled} {
		calls := 0
		h, err := NewSubjectHandler(subjectSubmitFunc(func(_ context.Context, event trigger.SubjectEvent) error {
			calls++
			require.Equal(t, "space", event.SpaceID)
			require.Equal(t, "ready", event.EventID)
			require.True(t, proto.Equal(payload, event.Ready))
			return failure
		}))
		require.NoError(t, err)
		result := h.Handle(context.Background(), delivery)
		require.Equal(t, 1, calls)
		if failure == nil {
			require.Equal(t, jetstream.ACK, result.Decision)
		} else {
			require.Equal(t, jetstream.RETRY, result.Decision)
			require.ErrorIs(t, result.Err, failure)
		}
		require.Equal(t, jetstream.TERM, h.Handle(context.Background(), nil).Decision)
		require.Equal(t, jetstream.TERM, h.Handle(context.Background(), &jetstream.Delivery{RawData: []byte("bad")}).Decision)
		require.Equal(t, 1, calls)
	}
}
