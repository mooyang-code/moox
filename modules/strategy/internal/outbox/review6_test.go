package outbox

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// erroringEventPublisher 让发布返回给定的错误。
type erroringEventPublisher struct{ err error }

func (p erroringEventPublisher) PublishMessage(context.Context, *eventpb.EventMessage) (*jetstream.PublishAck, error) {
	return nil, p.err
}

// 超过 EventBus 最大消息长度（本地预检返回 ErrInvalidMessage，服务端拒绝返回 ErrMaxPayload）是确定性错误：按永久错误
// 隔离，不能当成连接故障一直挡在队首；超时等传输错误仍可重试。
func TestJetStreamPublisherClassifiesOversizedEvents(t *testing.T) {
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	bar := timestamppb.New(time.Now().UTC())
	data, err := registry.MarshalMessage(events.LogicalAccountTargetWeightRequested, &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId: "request-1", InstanceId: "runner-1", StrategyId: "strategy-1", SessionId: "session-1", LogicalAccountId: "logical-1", BarEndTime: bar, EffectiveAt: bar, ValidUntil: timestamppb.New(time.Now().UTC().Add(time.Hour)),
		Targets: []*tradeeventpb.InstrumentWeightTarget{{InstrumentId: "BTC-USDT-SPOT", TargetWeight: "1"}},
	}, events.PublishOptions{EventID: "request-1", OccurredAt: time.Now().UTC(), SpaceID: "crypto", SubjectID: "logical-1"})
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		err       error
		permanent bool
	}{
		"本地预检超长":  {err: fmt.Errorf("%w: payload size 2444 exceeds 1024", jetstream.ErrInvalidMessage), permanent: true},
		"服务端拒绝超长": {err: fmt.Errorf("%w: publish: %w", jetstream.ErrConnection, nats.ErrMaxPayload), permanent: true},
		"发布超时":    {err: fmt.Errorf("%w: nats: timeout", jetstream.ErrPublishTimeout), permanent: false},
		"连接断开":    {err: fmt.Errorf("%w: connection closed", jetstream.ErrConnection), permanent: false},
	} {
		publisher := &JetStreamPublisher{Publisher: erroringEventPublisher{err: tc.err}, InstanceID: "strategy-1"}
		err := publisher.Publish(context.Background(), "request-1", data)
		var permanent *PermanentPublishError
		if errors.As(err, &permanent) != tc.permanent {
			t.Fatalf("%s：永久错误判定应为 %v：%v", name, tc.permanent, err)
		}
	}
}
