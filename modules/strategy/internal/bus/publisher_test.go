package bus

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
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
	data, err := registry.MarshalMessage(events.TradeRebalanceRequested, &tradeeventpb.RebalanceRequested{
		RequestId: "request-1", StrategyRunId: "run-1", ExecutionBindingId: "execution-1",
		AccountId: "account-1", ChannelId: "channel-1", Mode: "paper", DataRevision: "revision-1",
		CapitalAmount: "100", QuoteAsset: "USDT", CommandSequence: 1,
	}, events.PublishOptions{EventID: "request-1", OccurredAt: time.Now().UTC(), SpaceID: "crypto", SubjectID: "execution-1"})
	require.NoError(t, err)
	require.NoError(t, publisher.Publish(context.Background(), domain.OutboxMessage{MessageID: "request-1", EventData: data}))
	registry, err = events.DefaultRegistry()
	require.NoError(t, err)
	_, payload, err := events.DecodeRaw(registry, client.body, client.subject, client.id, events.ContentType)
	require.NoError(t, err)
	if payload.ProtoReflect().Descriptor().FullName() != "trpc.moox.trade.event.RebalanceRequested" {
		t.Fatalf("payload type = %s", payload.ProtoReflect().Descriptor().FullName())
	}
}
