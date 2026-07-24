package bus

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type captureEventPublisher struct {
	ready       bool
	subject, id string
	body        []byte
}

func (c *captureEventPublisher) Publish(_ context.Context, event events.EventType, payload proto.Message, opts events.PublishOptions) (*jetstream.PublishAck, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	encoded, err := registry.Encode(event, payload, opts)
	if err != nil {
		return nil, err
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(encoded.Message)
	if err != nil {
		return nil, err
	}
	c.subject, c.id, c.body = encoded.Subject, opts.EventID, body
	return &jetstream.PublishAck{Stream: "MOOX_STRATEGY"}, nil
}

func TestJetStreamPublisherBuildsEventMessage(t *testing.T) {
	client := &captureEventPublisher{}
	publisher := &JetStreamPublisher{Publisher: client, InstanceID: "strategy-1", Now: func() time.Time { return time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC) }}
	require.NoError(t, publisher.Publish(context.Background(), domain.OutboxMessage{MessageID: "run-1", Topic: "moox.strategy.output.accepted.v1", Payload: []byte(`{"space_id":"crypto","strategy_id":"s1","action":"hold"}`)}))
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	_, payload, err := events.DecodeRaw(registry, client.body, client.subject, client.id, events.ContentType)
	require.NoError(t, err)
	if payload.ProtoReflect().Descriptor().FullName() != "google.protobuf.Struct" {
		t.Fatalf("payload type = %s", payload.ProtoReflect().Descriptor().FullName())
	}
}
