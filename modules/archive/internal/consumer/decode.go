package consumer

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
)

type Decision uint8

const (
	storageFieldsChangedTopicPrefix = "moox.storage.fields_changed.v1."
	storageFieldsChangedMessageType = "moox.storage.fields_changed.v1"
	storageFieldsChangedContentType = "application/x-protobuf; message=trpc.moox.storage.DatasetFieldsChanged"
)

const (
	DecisionArchive Decision = iota + 1
	DecisionIgnore
	DecisionReject
)

type Decoder struct {
	sources map[string]map[string]struct{}
}

func NewDecoder(sources map[string][]string) *Decoder {
	allowed := make(map[string]map[string]struct{}, len(sources))
	for space, datasets := range sources {
		allowed[space] = make(map[string]struct{}, len(datasets))
		for _, dataset := range datasets {
			allowed[space][dataset] = struct{}{}
		}
	}
	return &Decoder{sources: allowed}
}

func (d *Decoder) Decode(message *messagepb.MooxMessage) (domain.EventBatch, Decision, error) {
	if message == nil {
		return domain.EventBatch{}, DecisionReject, fmt.Errorf("message is nil")
	}
	if message.GetProtocolVersion() != 1 || message.GetKind() != messagepb.MessageKind_MESSAGE_KIND_EVENT {
		return domain.EventBatch{}, DecisionReject, fmt.Errorf("unsupported message protocol or kind")
	}
	if !strings.HasPrefix(message.GetTopic(), storageFieldsChangedTopicPrefix) || message.GetMessageType() != storageFieldsChangedMessageType || message.GetContentType() != storageFieldsChangedContentType {
		return domain.EventBatch{}, DecisionReject, fmt.Errorf("unexpected topic, content type, or message type")
	}
	if strings.TrimSpace(message.GetMessageId()) == "" {
		return domain.EventBatch{}, DecisionReject, fmt.Errorf("message_id is required")
	}
	var event storagepb.DatasetFieldsChanged
	if err := proto.Unmarshal(message.GetPayload(), &event); err != nil {
		return domain.EventBatch{}, DecisionReject, fmt.Errorf("decode time-series event: %w", err)
	}
	if _, ok := d.sources[event.GetSpaceId()][event.GetDatasetId()]; !ok {
		return domain.EventBatch{}, DecisionIgnore, nil
	}
	if message.GetOccurredAt() == nil || message.GetOccurredAt().CheckValid() != nil {
		return domain.EventBatch{}, DecisionReject, fmt.Errorf("occurred_at is required")
	}
	writtenAt := message.GetOccurredAt().AsTime().UTC()
	rows := make(map[string]domain.RowPatch)
	for _, row := range event.GetRows() {
		patch, err := decodeRow(&event, row, writtenAt)
		if err != nil {
			return domain.EventBatch{}, DecisionReject, err
		}
		id := domain.PartitionID(patch.Partition) + "/" + domain.LogicalRowID(patch.DataTime, patch.DimensionsJSON)
		if previous, ok := rows[id]; ok {
			rows[id] = mergePatch(previous, patch)
		} else {
			rows[id] = patch
		}
	}
	if len(rows) == 0 {
		// Archive is an append-only historical sink. A committed DELETE is
		// intentionally acknowledged without creating a tombstone.
		return domain.EventBatch{MessageID: message.GetMessageId()}, DecisionIgnore, nil
	}
	batch := domain.EventBatch{MessageID: message.GetMessageId(), Rows: make([]domain.RowPatch, 0, len(rows))}
	for _, patch := range rows {
		batch.Rows = append(batch.Rows, patch)
	}
	return batch, DecisionArchive, nil
}

func decodeRow(event *storagepb.DatasetFieldsChanged, row *storagepb.RowFieldUpsert, writtenAt time.Time) (domain.RowPatch, error) {
	if row == nil || row.GetKey() == nil {
		return domain.RowPatch{}, fmt.Errorf("row key is required")
	}
	rowKey := row.GetKey()
	key := rowKey.GetTimeSeries()
	if key == nil || rowKey.GetSpaceId() != event.GetSpaceId() || rowKey.GetDatasetId() != event.GetDatasetId() {
		return domain.RowPatch{}, fmt.Errorf("row identity mismatch")
	}
	dataTime, err := parseTime(key.GetDataTime())
	if err != nil {
		return domain.RowPatch{}, fmt.Errorf("data_time: %w", err)
	}
	if key.GetSubjectId() == "" || key.GetFreq() == "" {
		return domain.RowPatch{}, fmt.Errorf("subject_id and freq are required")
	}
	dimensions, err := domain.CanonicalStringMap(nil)
	if err != nil {
		return domain.RowPatch{}, err
	}
	columns := make(map[string]domain.Scalar, len(row.GetFields()))
	for _, field := range row.GetFields() {
		column := &storagepb.ColumnValue{ColumnName: field.GetFieldId(), Value: field.GetValue(), ValueType: typedValueType(field.GetValue())}
		if _, exists := columns[column.GetColumnName()]; exists {
			return domain.RowPatch{}, fmt.Errorf("duplicate column %q", column.GetColumnName())
		}
		scalar, err := domain.ScalarFromColumn(column)
		if err != nil {
			return domain.RowPatch{}, err
		}
		columns[column.GetColumnName()] = scalar
	}
	attributes := make(map[string]string, len(row.GetAttributes()))
	for k, v := range row.GetAttributes() {
		attributes[k] = typedValueString(v)
	}
	return domain.RowPatch{Partition: domain.PartitionKey{SpaceID: event.GetSpaceId(), DatasetID: event.GetDatasetId(), SubjectID: key.GetSubjectId(), Freq: key.GetFreq(), Month: domain.MonthOf(dataTime)}, DataTime: dataTime, DimensionsJSON: dimensions, Attributes: attributes, WrittenAt: writtenAt, Columns: columns}, nil
}

func typedValueType(value *storagepb.TypedValue) storagepb.FieldValueType {
	switch value.GetValue().(type) {
	case *storagepb.TypedValue_StringValue:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
	case *storagepb.TypedValue_IntValue:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_INT
	case *storagepb.TypedValue_DoubleValue:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
	case *storagepb.TypedValue_BoolValue:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL
	case *storagepb.TypedValue_TimeValue:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
	case *storagepb.TypedValue_JsonValue:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON
	case *storagepb.TypedValue_BytesValue:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_BYTES
	default:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED
	}
}

func typedValueString(value *storagepb.TypedValue) string {
	switch value.GetValue().(type) {
	case *storagepb.TypedValue_StringValue:
		return value.GetStringValue()
	case *storagepb.TypedValue_IntValue:
		return fmt.Sprint(value.GetIntValue())
	case *storagepb.TypedValue_DoubleValue:
		return fmt.Sprint(value.GetDoubleValue())
	case *storagepb.TypedValue_BoolValue:
		return fmt.Sprint(value.GetBoolValue())
	default:
		return ""
	}
}

func parseTime(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, fmt.Errorf("time is required")
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
func mergePatch(a, b domain.RowPatch) domain.RowPatch {
	for k, v := range b.Columns {
		a.Columns[k] = v
	}
	for k, v := range b.Attributes {
		a.Attributes[k] = v
	}
	a.WrittenAt = b.WrittenAt
	return a
}
