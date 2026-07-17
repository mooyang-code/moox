package bus

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
)

type captureJetStreamClient struct {
	message *messagepb.MooxMessage
	ready   bool
}

func (c *captureJetStreamClient) Publish(_ context.Context, message *messagepb.MooxMessage, _ ...jetstream.PublishOption) (*jetstream.PublishAck, error) {
	c.message = message
	return &jetstream.PublishAck{}, nil
}
func (c *captureJetStreamClient) Ready() bool  { return c.ready }
func (c *captureJetStreamClient) Close() error { c.ready = false; return nil }

func TestJetStreamPublisherBuildsSharedEnvelope(t *testing.T) {
	client := &captureJetStreamClient{ready: true}
	occurred := time.Unix(100, 0).UTC()
	published := time.Unix(200, 0).UTC()
	publisher := &JetStreamPublisher{Client: client, InstanceID: "strategy-1", Now: func() time.Time { return published }}
	row := domain.OutboxMessage{MessageID: "run-1", Topic: "moox.strategy.action.accepted.v1", Payload: []byte(`{"action":"hold"}`), CreatedAt: occurred}
	if err := publisher.Publish(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	message := client.message
	if message.GetMessageId() != row.MessageID || message.GetTopic() != row.Topic || string(message.GetPayload()) != string(row.Payload) {
		t.Fatalf("message=%+v", message)
	}
	if message.GetProtocolVersion() != jetstream.ProtocolVersion || message.GetKind() != messagepb.MessageKind_MESSAGE_KIND_EVENT {
		t.Fatalf("protocol envelope=%+v", message)
	}
	if message.GetProducer().GetServiceName() != "moox-strategy" || message.GetProducer().GetInstanceId() != "strategy-1" {
		t.Fatalf("producer=%+v", message.GetProducer())
	}
	if message.GetContentType() != "application/json" || !message.GetOccurredAt().AsTime().Equal(occurred) || !message.GetPublishedAt().AsTime().Equal(published) {
		t.Fatalf("metadata=%+v", message)
	}
}
