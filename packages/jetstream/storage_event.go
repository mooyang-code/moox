package jetstream

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/messagepb"
)

const (
	StorageRowsUpsertedContentType = "application/x-protobuf; message=trpc.moox.storage.RowsUpserted"
	StorageRowsUpsertedMessageType = "moox.storage.rows_upserted.v1"
	StorageRowsUpsertedTopicPrefix = "moox.storage.rows_upserted.v1."
)

// ValidateStorageRowsUpsertedTopic validates the concrete two-token storage
// subject and returns the identifiers encoded in those tokens.
func ValidateStorageRowsUpsertedTopic(topic string) (string, string, error) {
	parts := strings.Split(topic, ".")
	if len(parts) != 6 || strings.Join(parts[:4], ".") != strings.TrimSuffix(StorageRowsUpsertedTopicPrefix, ".") {
		return "", "", fmt.Errorf("storage rows_upserted topic must contain exactly two subject tokens")
	}
	spaceID, err := DecodeSubjectToken(parts[4])
	if err != nil {
		return "", "", fmt.Errorf("space token: %w", err)
	}
	datasetID, err := DecodeSubjectToken(parts[5])
	if err != nil {
		return "", "", fmt.Errorf("dataset token: %w", err)
	}
	return spaceID, datasetID, nil
}

// ValidateStorageRowsUpsertedEnvelope validates the storage-specific
// content, event kind, and topic contract. Protocol validation remains at the
// generic envelope boundary.
func ValidateStorageRowsUpsertedEnvelope(msg *messagepb.MooxMessage) (string, string, error) {
	if msg == nil {
		return "", "", fmt.Errorf("storage envelope is nil")
	}
	if msg.GetKind() != messagepb.MessageKind_MESSAGE_KIND_EVENT {
		return "", "", fmt.Errorf("unexpected storage message kind %q", msg.GetKind())
	}
	if msg.GetContentType() != StorageRowsUpsertedContentType {
		return "", "", fmt.Errorf("unexpected storage content type %q", msg.GetContentType())
	}
	if msg.GetMessageType() != StorageRowsUpsertedMessageType {
		return "", "", fmt.Errorf("unexpected storage message type %q", msg.GetMessageType())
	}
	return ValidateStorageRowsUpsertedTopic(msg.GetTopic())
}
