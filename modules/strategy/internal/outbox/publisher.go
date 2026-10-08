package outbox

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
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
}

type EventPublisher interface {
	PublishMessage(context.Context, *eventpb.EventMessage) (*jetstream.PublishAck, error)
}

// PermanentPublishError identifies an event that can never be accepted by the
// current protocol (for example corrupt bytes or an unknown event type). The
// relay quarantines such rows instead of retrying the same prefix forever.
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
	_, err = p.Publisher.PublishMessage(ctx, message)
	if err != nil {
		// NATS rejects an over-sized payload locally/server-side and retrying it
		// cannot succeed without changing the event. Keep ordinary transport
		// errors retryable, but quarantine this deterministic protocol failure.
		text := strings.ToLower(err.Error())
		if strings.Contains(text, "maximum payload") || strings.Contains(text, "message size") {
			return &PermanentPublishError{Err: err}
		}
	}
	return err
}

// PublishResult 发布一条待投递结果携带的事件。
func (p *JetStreamPublisher) PublishResult(ctx context.Context, row store.Result) error {
	return p.Publish(ctx, row.ResultID, row.EventData)
}
