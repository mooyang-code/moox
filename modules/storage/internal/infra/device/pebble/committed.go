package pebble

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/factkey"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	timeSeriesRowsCommittedType = "moox.storage.time_series.rows_committed.v1"
	recordRowsCommittedType     = "moox.storage.record.rows_committed.v1"
	reservedDimensionsAttribute = "__moox_time_series_dimensions"
)

// WriteRowsWithCommittedMessage keeps message creation inside the DataShard.
// The message is initially a patch; writeRows replaces its rows with the
// complete post-merge snapshots before committing the fact and outbox batch.
func (s *Store) WriteRowsWithCommittedMessage(ctx context.Context, rows []*pb.PrimaryStoreRow) error {
	if len(rows) == 0 {
		return nil
	}
	if err := validateRowBatchIdentity(rows); err != nil {
		return err
	}
	now := time.Now().UTC()
	id, err := newMessageID()
	if err != nil {
		return err
	}
	var payload proto.Message
	messageType := ""
	topic := ""
	if rows[0].GetKey().GetDataKind() == pb.DataKind_DATA_KIND_TIME_SERIES {
		event := &pb.TimeSeriesRowsCommitted{ShardId: s.shardID, SpaceId: rows[0].GetKey().GetSpaceId(), DatasetId: rows[0].GetKey().GetDatasetId()}
		for _, row := range rows {
			public, err := primaryTimeSeriesRow(row)
			if err != nil {
				return err
			}
			event.Writes = append(event.Writes, &pb.TimeSeriesRowWrite{Operation: pb.RowWriteOperation_ROW_WRITE_OPERATION_MERGE, Row: public})
		}
		payload, messageType = event, timeSeriesRowsCommittedType
	} else if rows[0].GetKey().GetDataKind() == pb.DataKind_DATA_KIND_RECORD {
		event := &pb.RecordRowsCommitted{ShardId: s.shardID, SpaceId: rows[0].GetKey().GetSpaceId(), DatasetId: rows[0].GetKey().GetDatasetId()}
		for _, row := range rows {
			public, err := primaryRecordRow(row)
			if err != nil {
				return err
			}
			event.Writes = append(event.Writes, &pb.RecordRowWrite{Operation: pb.RowWriteOperation_ROW_WRITE_OPERATION_MERGE, Row: public})
		}
		payload, messageType = event, recordRowsCommittedType
	} else {
		return errors.New("rows must use a supported data kind")
	}
	shardToken, err := jetstream.EncodeShardToken(s.shardID)
	if err != nil {
		return err
	}
	if messageType == timeSeriesRowsCommittedType {
		topic = "moox.storage.rows_committed.time_series.v1." + shardToken
	} else {
		topic = "moox.storage.rows_committed.record.v1." + shardToken
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return err
	}
	msg := &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       id,
		Topic:           topic,
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        &messagepb.Producer{ServiceName: "moox-storage-datashard", InstanceId: s.shardID},
		SpaceId:         rows[0].GetKey().GetSpaceId(),
		OccurredAt:      timestamppb.New(now),
		PublishedAt:     timestamppb.New(now),
		ContentType:     committedContentType(messageType),
		MessageType:     messageType,
		Payload:         raw,
	}
	return s.WriteRowsWithOutbox(ctx, rows, &device.OutboxEntry{Data: mustMarshalMessage(msg), CreatedAt: now})
}

func validateRowBatchIdentity(rows []*pb.PrimaryStoreRow) error {
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		if err := validateRow(row); err != nil {
			return err
		}
	}
	first := rows[0].GetKey()
	for _, row := range rows[1:] {
		key := row.GetKey()
		if key.GetDataKind() != first.GetDataKind() || key.GetSpaceId() != first.GetSpaceId() || key.GetDatasetId() != first.GetDatasetId() {
			return errors.New("one DataShard batch must contain one data kind, space_id, and dataset_id")
		}
	}
	return nil
}

func (s *Store) DeleteRowsWithCommittedMessage(ctx context.Context, keys []*pb.PrimaryStoreKey) error {
	if len(keys) == 0 {
		return nil
	}
	if err := validateKeyBatchIdentity(keys); err != nil {
		return err
	}
	now := time.Now().UTC()
	id, err := newMessageID()
	if err != nil {
		return err
	}
	shardToken, err := jetstream.EncodeShardToken(s.shardID)
	if err != nil {
		return err
	}
	if keys[0].GetDataKind() == pb.DataKind_DATA_KIND_TIME_SERIES {
		event := &pb.TimeSeriesRowsCommitted{ShardId: s.shardID, SpaceId: keys[0].GetSpaceId(), DatasetId: keys[0].GetDatasetId()}
		for _, key := range keys {
			subject, freq, _, err := factkey.ParseTimeSeriesDataKey(key.GetKey())
			if err != nil {
				return err
			}
			event.Writes = append(event.Writes, &pb.TimeSeriesRowWrite{Operation: pb.RowWriteOperation_ROW_WRITE_OPERATION_DELETE, Row: &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{
				SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), SubjectId: subject, Freq: freq, DataTime: key.GetVersion(),
			}}})
		}
		raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(event)
		if err != nil {
			return err
		}
		msg := committedMessage(id, "moox.storage.rows_committed.time_series.v1."+shardToken, timeSeriesRowsCommittedType, keys[0].GetSpaceId(), now, raw, s.shardID)
		return s.deleteRows(ctx, keys, &device.OutboxEntry{Data: mustMarshalMessage(msg), CreatedAt: now})
	}
	if keys[0].GetDataKind() != pb.DataKind_DATA_KIND_RECORD {
		return errors.New("delete keys must use a supported data kind")
	}
	event := &pb.RecordRowsCommitted{ShardId: s.shardID, SpaceId: keys[0].GetSpaceId(), DatasetId: keys[0].GetDatasetId()}
	for _, key := range keys {
		event.Writes = append(event.Writes, &pb.RecordRowWrite{Operation: pb.RowWriteOperation_ROW_WRITE_OPERATION_DELETE, Row: &pb.RecordRow{Key: &pb.RecordKey{
			SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), RecordId: factkey.ParseRecordDataKey(key.GetKey()), Version: publicRecordVersion(key.GetVersion()),
		}}})
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(event)
	if err != nil {
		return err
	}
	msg := committedMessage(id, "moox.storage.rows_committed.record.v1."+shardToken, recordRowsCommittedType, keys[0].GetSpaceId(), now, raw, s.shardID)
	return s.deleteRows(ctx, keys, &device.OutboxEntry{Data: mustMarshalMessage(msg), CreatedAt: now})
}

func validateKeyBatchIdentity(keys []*pb.PrimaryStoreKey) error {
	if len(keys) == 0 {
		return nil
	}
	for _, key := range keys {
		if err := validateKey(key); err != nil {
			return err
		}
	}
	first := keys[0]
	for _, key := range keys[1:] {
		if key.GetDataKind() != first.GetDataKind() || key.GetSpaceId() != first.GetSpaceId() || key.GetDatasetId() != first.GetDatasetId() {
			return errors.New("one DataShard delete batch must contain one data kind, space_id, and dataset_id")
		}
	}
	return nil
}

func committedMessage(id, topic, messageType, spaceID string, now time.Time, payload []byte, shardID string) *messagepb.MooxMessage {
	return &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion, MessageId: id, Topic: topic,
		Kind:     messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer: &messagepb.Producer{ServiceName: "moox-storage-datashard", InstanceId: shardID},
		SpaceId:  spaceID, OccurredAt: timestamppb.New(now), PublishedAt: timestamppb.New(now),
		ContentType: committedContentType(messageType), MessageType: messageType, Payload: payload,
	}
}

func committedContentType(messageType string) string {
	if messageType == timeSeriesRowsCommittedType {
		return "application/x-protobuf; message=trpc.moox.storage.TimeSeriesRowsCommitted"
	}
	return "application/x-protobuf; message=trpc.moox.storage.RecordRowsCommitted"
}

func mustMarshalMessage(msg *messagepb.MooxMessage) []byte {
	raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	return raw
}

func newMessageID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "storage-" + hex.EncodeToString(raw[:]), nil
}

func primaryTimeSeriesRow(row *pb.PrimaryStoreRow) (*pb.TimeSeriesRow, error) {
	if row == nil || row.GetKey() == nil {
		return nil, errors.New("time-series row key is required")
	}
	subject, freq, _, err := factkey.ParseTimeSeriesDataKey(row.GetKey().GetKey())
	if err != nil {
		return nil, err
	}
	dimensions := map[string]string{}
	if raw := row.GetAttributes()[reservedDimensionsAttribute]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &dimensions); err != nil {
			return nil, err
		}
	}
	attributes := cloneAttributes(row.GetAttributes())
	delete(attributes, reservedDimensionsAttribute)
	return &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{
		SpaceId: row.GetKey().GetSpaceId(), DatasetId: row.GetKey().GetDatasetId(), SubjectId: subject, Freq: freq,
		Dimensions: dimensions, DataTime: row.GetKey().GetVersion(),
	}, Columns: cloneColumns(row.GetColumns()), Attributes: attributes,
		AttributesToDelete: append([]string(nil), row.GetAttributesToDelete()...),
		RemovedColumnNames: append([]string(nil), row.GetRemovedColumnNames()...)}, nil
}

func primaryRecordRow(row *pb.PrimaryStoreRow) (*pb.RecordRow, error) {
	if row == nil || row.GetKey() == nil {
		return nil, errors.New("record row key is required")
	}
	return &pb.RecordRow{Key: &pb.RecordKey{
		SpaceId: row.GetKey().GetSpaceId(), DatasetId: row.GetKey().GetDatasetId(), RecordId: factkey.ParseRecordDataKey(row.GetKey().GetKey()),
		Version: publicRecordVersion(row.GetKey().GetVersion()),
	}, Columns: cloneColumns(row.GetColumns()), Attributes: cloneAttributes(row.GetAttributes()),
		AttributesToDelete: append([]string(nil), row.GetAttributesToDelete()...),
		RemovedColumnNames: append([]string(nil), row.GetRemovedColumnNames()...)}, nil
}

func publicRecordVersion(version string) string {
	if strings.TrimSpace(version) == factkey.EmptyVersion {
		return ""
	}
	return version
}
