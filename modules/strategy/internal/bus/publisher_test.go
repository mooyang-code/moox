package bus

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/strategyeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type captureEventPublisher struct {
	ready       bool
	subject, id string
	body        []byte
}

func (c *captureEventPublisher) PublishMessage(_ context.Context, message *eventpb.EventMessage) (*jetstream.PublishAck, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	subject, err := registry.SubjectForMessage(message)
	if err != nil {
		return nil, err
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(message)
	if err != nil {
		return nil, err
	}
	c.subject, c.id, c.body = subject, message.GetEventId(), body
	return &jetstream.PublishAck{Stream: "MOOX_STRATEGY"}, nil
}

func TestJetStreamPublisherBuildsEventMessage(t *testing.T) {
	client := &captureEventPublisher{}
	publisher := &JetStreamPublisher{Publisher: client, InstanceID: "strategy-1", Now: func() time.Time { return time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC) }}
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	data, err := registry.MarshalMessage(events.StrategyOutputAccepted, &strategyeventpb.StrategyOutputAccepted{RunId: "run-1", BindingId: "binding-1", StrategyId: "s1", Action: "hold"}, events.PublishOptions{EventID: "run-1", OccurredAt: time.Now().UTC(), SpaceID: "crypto", SubjectID: "binding-1"})
	require.NoError(t, err)
	require.NoError(t, publisher.Publish(context.Background(), domain.OutboxMessage{MessageID: "run-1", EventData: data}))
	registry, err = events.DefaultRegistry()
	require.NoError(t, err)
	_, payload, err := events.DecodeRaw(registry, client.body, client.subject, client.id, events.ContentType)
	require.NoError(t, err)
	if payload.ProtoReflect().Descriptor().FullName() != "trpc.moox.strategy.event.StrategyOutputAccepted" {
		t.Fatalf("payload type = %s", payload.ProtoReflect().Descriptor().FullName())
	}
}
