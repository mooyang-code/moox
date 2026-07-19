package pebble

import (
	"fmt"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const datasetFieldsChangedType = "trpc.moox.storage.DatasetFieldsChanged"

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
		MessageId:       xid.New().String(),
		Topic:           fmt.Sprintf("moox.storage.fields_changed.v1.%s.%s", spaceToken, datasetToken),
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		SpaceId:         spaceID,
		OccurredAt:      timestamppb.New(time.Now().UTC()),
		ContentType:     "application/protobuf",
		MessageType:     datasetFieldsChangedType,
		Payload:         payload,
		Producer:        &messagepb.Producer{ServiceName: "storage-node", NodeId: nodeID},
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(message)
}
