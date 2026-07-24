package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/dlqpb"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

type MessagePublisher interface {
	PublishMessage(context.Context, *eventpb.EventMessage) (*jetstream.PublishAck, error)
}

// PublishRejected is the only path that publishes a poison-message event. The
// original delivery is TERM-ed by the caller only after this returns nil.
func PublishRejected(ctx context.Context, publisher MessagePublisher, registry *Registry, delivery *jetstream.Delivery, reason, rejectedBy string) error {
	if publisher == nil {
		return fmt.Errorf("DLQ publisher is unavailable")
	}
	message, err := RejectedMessage(registry, delivery, reason, rejectedBy, "")
	if err != nil {
		return err
	}
	_, err = publisher.PublishMessage(ctx, message)
	return err
}

// RejectedMessage creates the governed DLQ event for both decoded and
// malformed deliveries. The original wire bytes are retained in the payload.
func RejectedMessage(registry *Registry, delivery *jetstream.Delivery, reason, service, instance string) (*eventpb.EventMessage, error) {
	if registry == nil {
		return nil, fmt.Errorf("event registry is nil")
	}
	id, subject, contentType, body, count := "invalid-envelope", "", "", []byte(nil), uint64(0)
	if delivery != nil {
		id, subject, contentType, body, count = delivery.RawMessageID, delivery.Subject, delivery.ContentType, append([]byte(nil), delivery.RawData...), delivery.DeliveryCount
	}
	if strings.TrimSpace(id) == "" {
		sum := sha256.Sum256(append([]byte(subject+"\x00"), body...))
		id = "invalid-envelope-" + hex.EncodeToString(sum[:8])
	}
	rejectedBy := strings.TrimSpace(service)
	if instance = strings.TrimSpace(instance); instance != "" {
		if rejectedBy == "" {
			rejectedBy = "unknown"
		}
		rejectedBy += "/" + instance
	}
	if rejectedBy == "" {
		rejectedBy = "unknown"
	}
	spaceID := "moox_system"
	if original := new(eventpb.EventMessage); len(body) > 0 && proto.Unmarshal(body, original) == nil && strings.TrimSpace(original.GetSpaceId()) != "" {
		spaceID = original.GetSpaceId()
	}
	sum := sha256.Sum256([]byte(rejectedBy))
	eventID := id + ".rejected." + hex.EncodeToString(sum[:6])
	payload := &dlqpb.RejectedMessage{
		RejectedBy:          rejectedBy,
		OriginalMessageId:   id,
		OriginalSubject:     subject,
		OriginalContentType: contentType,
		OriginalData:        body,
		Reason:              reason,
		DeliveryCount:       count,
	}
	encoded, err := registry.Encode(DLQMessageRejected, payload, PublishOptions{EventID: eventID, OccurredAt: time.Now().UTC(), SpaceID: spaceID, SubjectID: rejectedBy})
	if err != nil {
		return nil, err
	}
	return encoded.Message, nil
}
