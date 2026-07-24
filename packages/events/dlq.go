package events

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/dlqpb"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
)

// RejectedMessage creates the governed DLQ event for both decoded and
// malformed deliveries. The original wire bytes are retained in the payload.
func RejectedMessage(registry *Registry, delivery *jetstream.Delivery, reason, service, instance string) (*eventpb.EventMessage, error) {
	if registry == nil {
		return nil, fmt.Errorf("event registry is nil")
	}
	id, subject, body, count := "invalid-envelope", "", []byte(nil), uint64(0)
	if delivery != nil {
		id, subject, body, count = delivery.RawMessageID, delivery.Subject, append([]byte(nil), delivery.RawData...), delivery.DeliveryCount
	}
	if strings.TrimSpace(id) == "" {
		sum := sha256.Sum256(append([]byte(subject+"\x00"), body...))
		id = "invalid-envelope-" + hex.EncodeToString(sum[:8])
	}
	if strings.TrimSpace(service) == "" {
		service = "moox-monitor"
	}
	if strings.TrimSpace(instance) == "" {
		instance = "unknown"
	}
	encoded, err := registry.Encode(MessageRejected, &dlqpb.RejectedMessage{OriginalMessageId: id, OriginalTopic: subject, RejectionReason: reason, OriginalPayload: body, DeliveryCount: count}, PublishOptions{EventID: id + ".rejected", OccurredAt: time.Now().UTC(), SpaceID: "moox_system", SubjectID: service + "/" + instance})
	if err != nil {
		return nil, err
	}
	return encoded.Message, nil
}
