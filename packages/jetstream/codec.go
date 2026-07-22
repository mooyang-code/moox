package jetstream

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
)

func ValidateMessage(msg *messagepb.MooxMessage, maxPayload int) error {
	return validateMessage(msg, maxPayload, true)
}

// ValidateOutboxMessage validates an envelope before the relay assigns its
// first publication timestamp. PublishedAt may be absent, but every other
// field required by ValidateMessage must already be durable in the outbox.
func ValidateOutboxMessage(msg *messagepb.MooxMessage, maxPayload int) error {
	return validateMessage(msg, maxPayload, false)
}

func validateMessage(msg *messagepb.MooxMessage, maxPayload int, requirePublishedAt bool) error {
	if msg == nil {
		return fmt.Errorf("%w: message is nil", ErrInvalidMessage)
	}
	if msg.GetProtocolVersion() != ProtocolVersion {
		return fmt.Errorf("%w: protocol_version must be %d", ErrInvalidMessage, ProtocolVersion)
	}
	if strings.TrimSpace(msg.GetMessageId()) == "" {
		return fmt.Errorf("%w: message_id is required", ErrInvalidMessage)
	}
	if err := validateSubject(msg.GetTopic()); err != nil {
		return fmt.Errorf("%w: topic: %v", ErrInvalidMessage, err)
	}
	if msg.GetProducer() == nil || strings.TrimSpace(msg.GetProducer().GetServiceName()) == "" || strings.TrimSpace(msg.GetProducer().GetInstanceId()) == "" {
		return fmt.Errorf("%w: producer service_name and instance_id are required", ErrInvalidMessage)
	}
	if msg.GetOccurredAt() == nil {
		return fmt.Errorf("%w: occurred_at is required", ErrInvalidMessage)
	}
	if err := msg.GetOccurredAt().CheckValid(); err != nil {
		return fmt.Errorf("%w: occurred_at: %v", ErrInvalidMessage, err)
	}
	if requirePublishedAt && msg.GetPublishedAt() == nil {
		return fmt.Errorf("%w: published_at is required", ErrInvalidMessage)
	}
	if msg.GetPublishedAt() != nil {
		if err := msg.GetPublishedAt().CheckValid(); err != nil {
			return fmt.Errorf("%w: published_at: %v", ErrInvalidMessage, err)
		}
	}
	if strings.TrimSpace(msg.GetContentType()) == "" {
		return fmt.Errorf("%w: content_type is required", ErrInvalidMessage)
	}
	if strings.TrimSpace(msg.GetMessageType()) == "" {
		return fmt.Errorf("%w: message_type is required", ErrInvalidMessage)
	}
	if len(msg.GetPayload()) == 0 {
		return fmt.Errorf("%w: payload is required", ErrInvalidMessage)
	}
	if maxPayload > 0 && len(msg.GetPayload()) > maxPayload {
		return fmt.Errorf("%w: payload size %d exceeds %d", ErrInvalidMessage, len(msg.GetPayload()), maxPayload)
	}
	return nil
}

func validateSubject(subject string) error {
	trimmed := strings.TrimSpace(subject)
	if subject != trimmed {
		return fmt.Errorf("subject cannot have leading or trailing whitespace")
	}
	subject = trimmed
	if subject == "" {
		return fmt.Errorf("subject is required")
	}
	for _, r := range subject {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '*' || r == '>' {
			return fmt.Errorf("subject must be a concrete token sequence")
		}
	}
	for _, token := range strings.Split(subject, ".") {
		if token == "" {
			return fmt.Errorf("subject contains an empty token")
		}
	}
	return nil
}

func marshalMessage(msg *messagepb.MooxMessage, maxPayload int) ([]byte, error) {
	if err := ValidateMessage(msg, maxPayload); err != nil {
		return nil, err
	}
	raw, err := (proto.MarshalOptions{Deterministic: true}).Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal: %v", ErrInvalidMessage, err)
	}
	if maxPayload > 0 && len(raw) > maxPayload {
		return nil, fmt.Errorf("%w: encoded message size %d exceeds %d", ErrInvalidMessage, len(raw), maxPayload)
	}
	return raw, nil
}

func unmarshalMessage(data []byte, maxPayload int) (*messagepb.MooxMessage, error) {
	if maxPayload > 0 && len(data) > maxPayload {
		return nil, fmt.Errorf("%w: encoded message size %d exceeds %d", ErrDecode, len(data), maxPayload)
	}
	copyData := append([]byte(nil), data...)
	msg := new(messagepb.MooxMessage)
	if err := proto.Unmarshal(copyData, msg); err != nil {
		return nil, fmt.Errorf("%w: protobuf: %v", ErrDecode, err)
	}
	if err := ValidateMessage(msg, maxPayload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecode, err)
	}
	msg.Payload = append([]byte(nil), msg.Payload...)
	return msg, nil
}

func decodeNATSMessage(msg *nats.Msg, maxPayload int) (*messagepb.MooxMessage, error) {
	if msg == nil {
		return nil, fmt.Errorf("%w: NATS message is nil", ErrDecode)
	}
	if got := msg.Header.Get("Content-Type"); got != OuterContentType {
		return nil, fmt.Errorf("%w: unexpected outer content type %q", ErrDecode, got)
	}
	decoded, err := unmarshalMessage(msg.Data, maxPayload)
	if err != nil {
		return nil, err
	}
	if decoded.GetTopic() != msg.Subject {
		return nil, fmt.Errorf("%w: topic %q does not match subject %q", ErrDecode, decoded.GetTopic(), msg.Subject)
	}
	if id := msg.Header.Get(nats.MsgIdHdr); id == "" || id != decoded.GetMessageId() {
		return nil, fmt.Errorf("%w: Nats-Msg-Id does not match message_id", ErrDecode)
	}
	return decoded, nil
}
