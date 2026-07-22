package jetstream

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/messagepb"
)

const (
	StorageFieldsChangedContentType = "application/x-protobuf; message=trpc.moox.storage.DatasetFieldsChanged"
	StorageFieldsChangedMessageType = "moox.storage.fields_changed.v1"
	StorageFieldsChangedTopicPrefix = "moox.storage.fields_changed.v1."
)

// ValidateStorageFieldsChangedTopic validates the concrete two-token storage
// subject and returns the identifiers encoded in those tokens.
func ValidateStorageFieldsChangedTopic(topic string) (string, string, error) {
	parts := strings.Split(topic, ".")
	if len(parts) != 6 || strings.Join(parts[:4], ".") != strings.TrimSuffix(StorageFieldsChangedTopicPrefix, ".") {
		return "", "", fmt.Errorf("storage fields_changed topic must contain exactly two subject tokens")
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

// ValidateStorageFieldsChangedEnvelope validates the storage-specific
// content and topic contract. Generic protocol and kind checks remain at the
// consumer boundary because different consumers own those semantics.
func ValidateStorageFieldsChangedEnvelope(msg *messagepb.MooxMessage) (string, string, error) {
	if msg == nil {
		return "", "", fmt.Errorf("storage envelope is nil")
	}
	if msg.GetContentType() != StorageFieldsChangedContentType {
		return "", "", fmt.Errorf("unexpected storage content type %q", msg.GetContentType())
	}
	if msg.GetMessageType() != StorageFieldsChangedMessageType {
		return "", "", fmt.Errorf("unexpected storage message type %q", msg.GetMessageType())
	}
	return ValidateStorageFieldsChangedTopic(msg.GetTopic())
}
