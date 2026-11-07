package dispatch

import (
	"testing"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/catalog"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func mustEntry(t *testing.T) catalog.Entry {
	t.Helper()
	entry, ok := catalog.Lookup(delistEvent)
	require.True(t, ok)
	return entry
}

// messageFor 返回一个已解码的下架事件信封，直接交给 accept，不经过 JetStream 编解码。
func messageFor(t *testing.T) *eventpb.EventMessage {
	t.Helper()
	return &eventpb.EventMessage{
		EventId: "event-1", EventName: delistEvent, EventVersion: 1, SpaceId: events.DelistSystemSpaceID,
		SubjectId: "binance:SPOT:FOOUSDT", OccurredAt: timestamppb.New(baseTime),
	}
}
