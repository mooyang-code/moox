package outbox

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
	"google.golang.org/protobuf/types/known/timestamppb"
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
	publisher := &JetStreamPublisher{Publisher: client, InstanceID: "strategy-1"}
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	validUntil := timestamppb.New(time.Now().UTC().Add(time.Hour))
	bar := timestamppb.New(time.Now().UTC())
	data, err := registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: "request-1", InstanceId: "runner-1", StrategyId: "strategy-1", SessionId: "session-1", LogicalAccountId: "logical-1", BarEndTime: bar, EffectiveAt: bar, ValidUntil: validUntil,
		CommandSequence: 1,
		Targets: []*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1",
		}},
	}, events.PublishOptions{EventID: "request-1", OccurredAt: time.Now().UTC(), SpaceID: "crypto", SubjectID: "logical-1"})
	require.NoError(t, err)
	require.NoError(t, publisher.Publish(context.Background(), domain.OutboxMessage{MessageID: "request-1", EventData: data}))
	registry, err = events.DefaultRegistry()
	require.NoError(t, err)
	_, payload, err := events.DecodeRaw(registry, client.body, client.subject, client.id, events.ContentType)
	require.NoError(t, err)
	if payload.ProtoReflect().Descriptor().FullName() != "trpc.moox.trade.event.LogicalAccountTargetWeightRequested" {
		t.Fatalf("payload type = %s", payload.ProtoReflect().Descriptor().FullName())
	}
}

func TestJetStreamPublisherAcceptsEmptyFullTargetWithoutExpiry(t *testing.T) {
	now := time.Now().UTC()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	bar := timestamppb.New(now)
	validUntil := timestamppb.New(now.Add(time.Hour))
	data, err := registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: "target-empty", InstanceId: "runner-1", StrategyId: "strategy-1", SessionId: "session-1", LogicalAccountId: "logical-1", BarEndTime: bar, EffectiveAt: bar, ValidUntil: validUntil,
		CommandSequence: 1, Targets: []*tradeeventpb.InstrumentWeightTarget{},
	}, events.PublishOptions{
		EventID: "target-empty", OccurredAt: now, SpaceID: "crypto", SubjectID: "logical-1",
	})
	require.NoError(t, err)
	publisher := &JetStreamPublisher{
		Publisher: &captureEventPublisher{},
	}
	require.NoError(t, publisher.Publish(context.Background(), domain.OutboxMessage{
		MessageID: "target-empty", EventData: data,
	}))
}
