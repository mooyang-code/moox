package pebble

import (
	"fmt"
	"log"
	"strconv"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const storageNodeServiceName = "storage-node"

// BuildDatasetFieldsChangedMessage creates the durable message persisted by a
// field write. It intentionally carries no sequence or source-node progress.
func BuildDatasetFieldsChangedMessage(nodeID string, spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
	if spaceID == "" || datasetID == "" {
		return nil, fmt.Errorf("space_id and dataset_id are required")
	}
	spaceToken, err := jetstream.EncodeSubjectToken(spaceID)
	if err != nil {
		return nil, err
	}
	datasetToken, err := jetstream.EncodeSubjectToken(datasetID)
	if err != nil {
		return nil, err
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(&pb.DatasetFieldsChanged{SpaceId: spaceID, DatasetId: datasetID, Rows: rows})
	if err != nil {
		return nil, err
	}
	message := &messagepb.MooxMessage{
		ProtocolVersion: 1,
		Topic:           fmt.Sprintf("%s%s.%s", jetstream.StorageFieldsChangedTopicPrefix, spaceToken, datasetToken),
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		SpaceId:         spaceID,
		OccurredAt:      timestamppb.New(time.Now().UTC()),
		ContentType:     jetstream.StorageFieldsChangedContentType,
		MessageType:     jetstream.StorageFieldsChangedMessageType,
		Payload:         payload,
		Producer:        &messagepb.Producer{ServiceName: storageNodeServiceName, InstanceId: nodeID, NodeId: nodeID},
	}
	if _, _, err := validateStorageFieldsChangedOutboxEnvelope(message); err != nil {
		return nil, err
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(message)
}

// BindOutboxID assigns the stable transport id after the atomic Pebble batch
// has reserved its internal outbox id.
func BindOutboxID(data []byte, nodeID string, outboxID uint64) ([]byte, error) {
	msg := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(data, msg); err != nil {
		log.Printf("storage outbox message %d has invalid envelope: %v", outboxID, err)
		return nil, fmt.Errorf("unmarshal storage outbox message %d: %w", outboxID, err)
	}
	token, err := jetstream.EncodeSubjectToken(nodeID)
	if err != nil {
		return nil, err
	}
	msg.MessageId = "storage-" + token + "-" + strconv.FormatUint(outboxID, 10)
	if msg.Producer == nil {
		msg.Producer = &messagepb.Producer{}
	}
	msg.Producer.ServiceName = storageNodeServiceName
	msg.Producer.InstanceId = nodeID
	msg.Producer.NodeId = nodeID
	if _, _, err := validateStorageFieldsChangedOutboxEnvelope(msg); err != nil {
		return nil, fmt.Errorf("validate storage fields_changed envelope %d: %w", outboxID, err)
	}
	if err := jetstream.ValidateOutboxMessage(msg, 0); err != nil {
		return nil, fmt.Errorf("validate storage outbox message %d: %w", outboxID, err)
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(msg)
}

func validateStorageFieldsChangedOutboxEnvelope(msg *messagepb.MooxMessage) (string, string, error) {
	spaceID, datasetID, err := jetstream.ValidateStorageFieldsChangedEnvelope(msg)
	if err != nil {
		return "", "", err
	}
	if msg.GetSpaceId() != spaceID {
		return "", "", fmt.Errorf("message space_id %q does not match topic space_id %q", msg.GetSpaceId(), spaceID)
	}
	event := &pb.DatasetFieldsChanged{}
	if err := proto.Unmarshal(msg.GetPayload(), event); err != nil {
		return "", "", fmt.Errorf("decode fields_changed payload: %w", err)
	}
	if event.GetSpaceId() != spaceID || event.GetDatasetId() != datasetID {
		return "", "", fmt.Errorf("fields_changed payload dataset identity does not match topic")
	}
	return spaceID, datasetID, nil
}
