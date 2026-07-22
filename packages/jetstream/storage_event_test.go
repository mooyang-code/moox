package jetstream

import (
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestValidateStorageFieldsChangedTopic(t *testing.T) {
	spaceToken, err := EncodeSubjectToken("crypto_binance")
	if err != nil {
		t.Fatal(err)
	}
	datasetToken, err := EncodeSubjectToken("spot_kline")
	if err != nil {
		t.Fatal(err)
	}
	valid := "moox.storage.fields_changed.v1." + spaceToken + "." + datasetToken
	spaceID, datasetID, err := ValidateStorageFieldsChangedTopic(valid)
	if err != nil || spaceID != "crypto_binance" || datasetID != "spot_kline" {
		t.Fatalf("ValidateStorageFieldsChangedTopic() = %q, %q, %v", spaceID, datasetID, err)
	}
	for _, topic := range []string{
		"moox.storage.fields_changed.v1." + spaceToken,
		valid + ".extra",
		"moox.storage.fields_changed.v1." + spaceToken + ".>",
		"moox.storage.fields_changed.v1." + spaceToken + ".*",
		"moox.storage.rows_committed.v1." + spaceToken + "." + datasetToken,
	} {
		if _, _, err := ValidateStorageFieldsChangedTopic(topic); err == nil {
			t.Fatalf("ValidateStorageFieldsChangedTopic(%q) succeeded", topic)
		}
	}
}

func TestValidateOutboxMessageAllowsOnlyMissingPublishedAt(t *testing.T) {
	msg := validTestMessage("id", "moox.storage.fields_changed.v1.mzxw6.mjqxe")
	msg.PublishedAt = nil
	if err := ValidateOutboxMessage(msg, 1024); err != nil {
		t.Fatalf("ValidateOutboxMessage() error = %v", err)
	}
	if err := ValidateMessage(msg, 1024); err == nil {
		t.Fatal("ValidateMessage() error = nil, want missing published_at rejection")
	}
	msg.OccurredAt = timestamppb.New(timestamppb.Now().AsTime())
	msg.Payload = nil
	if err := ValidateOutboxMessage(msg, 1024); err == nil {
		t.Fatal("ValidateOutboxMessage() error = nil, want payload rejection")
	}
	msg.Payload = []byte("payload")
	msg.Kind = 0
	if err := ValidateOutboxMessage(msg, 1024); err == nil {
		t.Fatal("ValidateOutboxMessage() accepted unspecified kind")
	}
}

func TestValidateStorageFieldsChangedEnvelope(t *testing.T) {
	msg := validTestMessage("id", "moox.storage.fields_changed.v1.mzxw6.mjqxe")
	msg.ContentType = StorageFieldsChangedContentType
	msg.MessageType = StorageFieldsChangedMessageType
	spaceID, datasetID, err := ValidateStorageFieldsChangedEnvelope(msg)
	if err != nil || spaceID != "foo" || datasetID != "bar" {
		t.Fatalf("ValidateStorageFieldsChangedEnvelope() = %q, %q, %v", spaceID, datasetID, err)
	}
	msg.Kind = 0
	if _, _, err := ValidateStorageFieldsChangedEnvelope(msg); err == nil {
		t.Fatal("ValidateStorageFieldsChangedEnvelope() accepted unspecified kind")
	}
}
