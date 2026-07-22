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

const datasetFieldsChangedContentType = "application/x-protobuf; message=trpc.moox.storage.DatasetFieldsChanged"
const datasetFieldsChangedMessageType = "moox.storage.fields_changed.v1"

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
		Topic:           fmt.Sprintf("moox.storage.fields_changed.v1.%s.%s", spaceToken, datasetToken),
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		SpaceId:         spaceID,
		OccurredAt:      timestamppb.New(time.Now().UTC()),
		ContentType:     datasetFieldsChangedContentType,
		MessageType:     datasetFieldsChangedMessageType,
		Payload:         payload,
		Producer:        &messagepb.Producer{ServiceName: "storage-node", NodeId: nodeID},
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(message)
}

// BindOutboxID assigns the stable transport id after the atomic Pebble batch
// has reserved its internal outbox id.
func BindOutboxID(data []byte, nodeID string, outboxID uint64) ([]byte, error) {
	msg := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(data, msg); err != nil {
		log.Printf("storage outbox message %d has invalid envelope: %v", outboxID, err)
		return data, nil
	}
	token, err := jetstream.EncodeSubjectToken(nodeID)
	if err != nil {
		return nil, err
	}
	msg.MessageId = "storage-" + token + "-" + strconv.FormatUint(outboxID, 10)
	return proto.MarshalOptions{Deterministic: true}.Marshal(msg)
}
