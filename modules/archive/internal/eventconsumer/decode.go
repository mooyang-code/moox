package eventconsumer

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	localpb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	sharedpb "github.com/mooyang-code/moox/packages/storagepb"
)

type Decision uint8

const (
	DecisionArchive Decision = iota + 1
	DecisionIgnore
	DecisionReject
)

type Decoder struct {
	sources map[string]map[string]struct{}
}

// DecodeEvent decodes the governed storage event directly.
func (d *Decoder) DecodeEvent(raw []byte, subject, messageID string) (domain.EventBatch, Decision, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return domain.EventBatch{}, DecisionReject, err
	}
	message, payload, err := events.DecodeDatasetRowsUpserted(registry, raw, subject, messageID)
	if err != nil {
		return domain.EventBatch{}, DecisionReject, err
	}
	return d.decodeRows(message.GetEventId(), message.GetSpaceId(), message.GetSubjectId(), payload, message.GetOccurredAt().AsTime().UTC())
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

func (d *Decoder) decodeRows(messageID, spaceID, datasetID string, event *sharedpb.DatasetRowsUpserted, writtenAt time.Time) (domain.EventBatch, Decision, error) {
	if event == nil {
		return domain.EventBatch{}, DecisionReject, fmt.Errorf("storage event is nil")
	}
	if event.GetSpaceId() != spaceID || event.GetDatasetId() != datasetID {
		return domain.EventBatch{}, DecisionReject, fmt.Errorf("storage topic and payload identity mismatch")
	}
	if _, ok := d.sources[event.GetSpaceId()][event.GetDatasetId()]; !ok {
		return domain.EventBatch{}, DecisionIgnore, nil
	}
	rows := make(map[string]domain.RowPatch)
	for _, row := range event.GetRows() {
		patch, err := decodeRow(event, row, writtenAt)
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
		return domain.EventBatch{MessageID: messageID}, DecisionIgnore, nil
	}
	batch := domain.EventBatch{MessageID: messageID, Rows: make([]domain.RowPatch, 0, len(rows))}
	for _, patch := range rows {
		batch.Rows = append(batch.Rows, patch)
	}
	return batch, DecisionArchive, nil
}

func decodeRow(event *sharedpb.DatasetRowsUpserted, row *sharedpb.RowUpsert, writtenAt time.Time) (domain.RowPatch, error) {
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
		if _, exists := columns[field.GetFieldId()]; exists {
			return domain.RowPatch{}, fmt.Errorf("duplicate field %q", field.GetFieldId())
		}
		scalar, err := domain.ScalarFromField(field.GetFieldId(), toLocalTypedValue(field.GetValue()))
		if err != nil {
			return domain.RowPatch{}, err
		}
		columns[field.GetFieldId()] = scalar
	}
	attributes := make(map[string]string, len(row.GetAttributes()))
	for k, v := range row.GetAttributes() {
		attributes[k] = typedValueString(v)
	}
	return domain.RowPatch{Partition: domain.PartitionKey{SpaceID: event.GetSpaceId(), DatasetID: event.GetDatasetId(), SubjectID: key.GetSubjectId(), Freq: key.GetFreq(), Month: domain.MonthOf(dataTime)}, DataTime: dataTime, DimensionsJSON: dimensions, Attributes: attributes, WrittenAt: writtenAt, Columns: columns}, nil
}

func typedValueString(value *sharedpb.TypedValue) string {
	switch value.GetValue().(type) {
	case *sharedpb.TypedValue_StringValue:
		return value.GetStringValue()
	case *sharedpb.TypedValue_IntValue:
		return fmt.Sprint(value.GetIntValue())
	case *sharedpb.TypedValue_DoubleValue:
		return fmt.Sprint(value.GetDoubleValue())
	case *sharedpb.TypedValue_BoolValue:
		return fmt.Sprint(value.GetBoolValue())
	default:
		return ""
	}
}

func toLocalTypedValue(value *sharedpb.TypedValue) *localpb.TypedValue {
	if value == nil {
		return nil
	}
	switch v := value.GetValue().(type) {
	case *sharedpb.TypedValue_StringValue:
		return &localpb.TypedValue{Value: &localpb.TypedValue_StringValue{StringValue: v.StringValue}}
	case *sharedpb.TypedValue_IntValue:
		return &localpb.TypedValue{Value: &localpb.TypedValue_IntValue{IntValue: v.IntValue}}
	case *sharedpb.TypedValue_DoubleValue:
		return &localpb.TypedValue{Value: &localpb.TypedValue_DoubleValue{DoubleValue: v.DoubleValue}}
	case *sharedpb.TypedValue_BoolValue:
		return &localpb.TypedValue{Value: &localpb.TypedValue_BoolValue{BoolValue: v.BoolValue}}
	case *sharedpb.TypedValue_TimeValue:
		return &localpb.TypedValue{Value: &localpb.TypedValue_TimeValue{TimeValue: v.TimeValue}}
	case *sharedpb.TypedValue_JsonValue:
		return &localpb.TypedValue{Value: &localpb.TypedValue_JsonValue{JsonValue: v.JsonValue}}
	case *sharedpb.TypedValue_BytesValue:
		return &localpb.TypedValue{Value: &localpb.TypedValue_BytesValue{BytesValue: append([]byte(nil), v.BytesValue...)}}
	default:
		return nil
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
