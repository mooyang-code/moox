package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type captureEventPublisher struct {
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
		Targets: []*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1",
		}},
	}, events.PublishOptions{EventID: "request-1", OccurredAt: time.Now().UTC(), SpaceID: "crypto", SubjectID: "logical-1"})
	require.NoError(t, err)
	require.NoError(t, publisher.Publish(context.Background(), "request-1", data))
	registry, err = events.DefaultRegistry()
	require.NoError(t, err)
	_, payload, err := events.DecodeRaw(registry, client.body, client.subject, client.id, events.ContentType)
	require.NoError(t, err)
	if payload.ProtoReflect().Descriptor().FullName() != "trpc.moox.trade.event.LogicalAccountTargetWeightRequested" {
		t.Fatalf("载荷类型不符：%s", payload.ProtoReflect().Descriptor().FullName())
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
		Targets: []*tradeeventpb.InstrumentWeightTarget{},
	}, events.PublishOptions{
		EventID: "target-empty", OccurredAt: now, SpaceID: "crypto", SubjectID: "logical-1",
	})
	require.NoError(t, err)
	publisher := &JetStreamPublisher{
		Publisher: &captureEventPublisher{},
	}
	require.NoError(t, publisher.Publish(context.Background(), "target-empty", data))
}

// hangingEventPublisher 模拟确认丢失：发布一直挂起，直到调用方的截止时间到达。
type hangingEventPublisher struct{}

func (hangingEventPublisher) PublishMessage(ctx context.Context, _ *eventpb.EventMessage) (*jetstream.PublishAck, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// 确认丢失时单次发布按超时返回，而不是让投递循环一直卡住；超时属于发布失败，由运行时断开重连后重发。
func TestJetStreamPublisherTimesOutWithoutAck(t *testing.T) {
	publisher := &JetStreamPublisher{Publisher: hangingEventPublisher{}, InstanceID: "strategy-1", Timeout: 50 * time.Millisecond}
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	bar := timestamppb.New(time.Now().UTC())
	data, err := registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: "request-1", InstanceId: "runner-1", StrategyId: "strategy-1", SessionId: "session-1", LogicalAccountId: "logical-1", BarEndTime: bar, EffectiveAt: bar, ValidUntil: timestamppb.New(time.Now().UTC().Add(time.Hour)),
		Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1"}},
	}, events.PublishOptions{EventID: "request-1", OccurredAt: time.Now().UTC(), SpaceID: "crypto", SubjectID: "logical-1"})
	require.NoError(t, err)
	started := time.Now()
	err = publisher.Publish(context.Background(), "request-1", data)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var permanent *PermanentPublishError
	require.False(t, errors.As(err, &permanent), "超时不是永久错误，结果应保持待投递")
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("发布超时后应尽快返回：%s", elapsed)
	}
}
