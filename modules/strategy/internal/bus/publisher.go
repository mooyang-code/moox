package bus

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type JetStreamClient interface {
	Publish(context.Context, *messagepb.MooxMessage, ...jetstream.PublishOption) (*jetstream.PublishAck, error)
	Ready() bool
	Close() error
}

type JetStreamPublisher struct {
	Client     JetStreamClient
	InstanceID string
	Now        func() time.Time
}

func (p *JetStreamPublisher) Publish(ctx context.Context, row domain.OutboxMessage) error {
	if p == nil || p.Client == nil {
		return errors.New("strategy JetStream publisher is unavailable")
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	occurredAt := row.CreatedAt.UTC()
	if occurredAt.IsZero() {
		occurredAt = now
	}
	instanceID := p.InstanceID
	if instanceID == "" {
		instanceID = "strategy"
	}
	_, err := p.Client.Publish(ctx, &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       row.MessageID,
		Topic:           row.Topic,
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        &messagepb.Producer{ServiceName: "moox-strategy", InstanceId: instanceID},
		OccurredAt:      timestamppb.New(occurredAt),
		PublishedAt:     timestamppb.New(now),
		ContentType:     "application/json",
		Payload:         row.Payload,
	})
	return err
}
