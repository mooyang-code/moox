package outbox

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

func ValidateJetStreamPublisher(_ context.Context, client JetStreamClient, _ string) error {
	if client == nil || !client.Ready() {
		return errors.New("策略 EventBus 发布器未就绪")
	}
	return nil
}

type JetStreamClient interface {
	Ready() bool
	Close() error
	EventPublisher() EventPublisher
}

type managedClient struct {
	client    *jetstream.Client
	publisher EventPublisher
}

func NewManagedClient(client *jetstream.Client) (JetStreamClient, error) {
	if client == nil {
		return nil, errors.New("策略 EventBus 客户端为空")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		return nil, err
	}
	return &managedClient{client: client, publisher: publisher}, nil
}

func (c *managedClient) Ready() bool                    { return c != nil && c.client != nil && c.client.Ready() }
func (c *managedClient) Close() error                   { return c.client.Close() }
func (c *managedClient) EventPublisher() EventPublisher { return c.publisher }

type JetStreamPublisher struct {
	Publisher  EventPublisher
	InstanceID string
	// Timeout 限制单次发布等待确认的时长：确认丢失（EventBus 重启、断网）时不设截止时间的发布会一直挂起，
	// 投递循环卡住后所有结果都停在 pending。超时按发布失败处理并重连，result_id 作为 Msg-Id，重发会被去重。
	Timeout time.Duration
}

// DefaultPublishTimeout 是未配置时的单次发布超时。
const DefaultPublishTimeout = 10 * time.Second

type EventPublisher interface {
	PublishMessage(context.Context, *eventpb.EventMessage) (*jetstream.PublishAck, error)
}

// PermanentPublishError 表示当前协议永远不会接受的事件（例如字节损坏、事件类型未知、超过最大消息长度）：
// 投递循环隔离这一行，而不是一直重试、挡住其后的结果。
type PermanentPublishError struct{ Err error }

func (e *PermanentPublishError) Error() string {
	if e == nil || e.Err == nil {
		return "策略事件永久发布失败"
	}
	return e.Err.Error()
}

func (e *PermanentPublishError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Publish 校验并发布一条已编码的事件；无法被当前协议接受的事件返回 *PermanentPublishError。
func (p *JetStreamPublisher) Publish(ctx context.Context, messageID string, eventData []byte) error {
	if p == nil || p.Publisher == nil {
		return errors.New("策略 JetStream 发布器不可用")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	rawMessage := new(eventpb.EventMessage)
	if err := proto.Unmarshal(eventData, rawMessage); err != nil {
		return &PermanentPublishError{Err: err}
	}
	message, err := registry.UnmarshalMessage(eventData)
	if err != nil {
		return &PermanentPublishError{Err: err}
	}
	if message.GetEventId() != messageID {
		return &PermanentPublishError{Err: errors.New("待投递事件的 event_id 与结果 ID 不一致")}
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultPublishTimeout
	}
	publishCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err = p.Publisher.PublishMessage(publishCtx, message)
	// 消息过不了发布前校验（例如超过 EventBus 的最大消息长度，本地预检或服务端拒绝）时原样重试永远不会成功，
	// 还会挡住其后所有实例的目标：按永久错误隔离；其余传输错误照常重试。
	if errors.Is(err, jetstream.ErrInvalidMessage) || errors.Is(err, nats.ErrMaxPayload) {
		return &PermanentPublishError{Err: err}
	}
	return err
}

// PublishResult 发布一条待投递结果携带的事件。
func (p *JetStreamPublisher) PublishResult(ctx context.Context, row store.Result) error {
	return p.Publish(ctx, row.ResultID, row.EventData)
}
