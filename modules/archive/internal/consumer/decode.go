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
	if !strings.HasPrefix(message.GetTopic(), "moox.storage.rows_committed.time_series.v1.") || message.GetMessageType() != "moox.storage.time_series.rows_committed.v1" {
		return domain.EventBatch{}, DecisionReject, fmt.Errorf("unexpected topic or content type")
	}
	if strings.TrimSpace(message.GetMessageId()) == "" {
		return domain.EventBatch{}, DecisionReject, fmt.Errorf("message_id is required")
	}
	var event storagepb.TimeSeriesRowsCommitted
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
	for _, write := range event.GetWrites() {
		if write.GetOperation() == storagepb.RowWriteOperation_ROW_WRITE_OPERATION_DELETE {
			continue
		}
		row := write.GetRow()
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

func decodeRow(event *storagepb.TimeSeriesRowsCommitted, row *storagepb.TimeSeriesRow, writtenAt time.Time) (domain.RowPatch, error) {
	if row == nil || row.GetKey() == nil {
		return domain.RowPatch{}, fmt.Errorf("row key is required")
	}
	key := row.GetKey()
	if key.GetSpaceId() != event.GetSpaceId() || key.GetDatasetId() != event.GetDatasetId() {
		return domain.RowPatch{}, fmt.Errorf("row identity mismatch")
	}
	dataTime, err := parseTime(key.GetDataTime())
	if err != nil {
		return domain.RowPatch{}, fmt.Errorf("data_time: %w", err)
	}
	if key.GetSubjectId() == "" || key.GetFreq() == "" {
		return domain.RowPatch{}, fmt.Errorf("subject_id and freq are required")
	}
	dimensions, err := domain.CanonicalStringMap(key.GetDimensions())
	if err != nil {
		return domain.RowPatch{}, err
	}
	columns := make(map[string]domain.Scalar, len(row.GetColumns()))
	for _, column := range row.GetColumns() {
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
		attributes[k] = v
	}
	return domain.RowPatch{Partition: domain.PartitionKey{SpaceID: event.GetSpaceId(), DatasetID: event.GetDatasetId(), SubjectID: key.GetSubjectId(), Freq: key.GetFreq(), Month: domain.MonthOf(dataTime)}, DataTime: dataTime, DimensionsJSON: dimensions, Attributes: attributes, WrittenAt: writtenAt, Columns: columns}, nil
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
