package storageio

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (c *Client) WriteFactorPatch(
	ctx context.Context,
	task *engine.FactorTask,
	result *engine.FactorResult,
) (uint64, error) {
	if task == nil || result == nil {
		return 0, fmt.Errorf("task and result are required")
	}
	if len(result.Rows) == 0 {
		return 0, nil
	}
	rows := make([]*storagepb.RowFieldUpsert, 0, len(result.Rows))
	computedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for i, resultRow := range result.Rows {
		if resultRow.DataTime.IsZero() {
			return 0, fmt.Errorf("factor result row %d data_time is required", i)
		}
		row := &storagepb.RowFieldUpsert{
			Key: &storagepb.RowKey{
				SpaceId: task.SpaceID, DatasetId: task.TargetDataset,
				Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
					SubjectId: task.SubjectID, Freq: task.Freq,
					DataTime:  resultRow.DataTime.UTC().Format(time.RFC3339Nano),
					SeriesTag: resultRow.SeriesTag,
				}},
			},
			Attributes: map[string]*storagepb.TypedValue{
				"factor.id":             stringValue(task.Factor.FactorID),
				"factor.source_hash":    stringValue(task.Factor.SourceHash),
				"factor.parent_task_id": stringValue(task.TaskID),
				"factor.computed_at":    stringValue(computedAt),
			},
		}
		for _, name := range task.Factor.Outputs {
			value, exists := resultRow.Values[name]
			if !exists {
				return 0, fmt.Errorf("factor result row %d is missing output %s", i, name)
			}
			if value == nil {
				row.Fields = append(row.Fields, &storagepb.FieldValue{
					FieldId: name,
					Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{
						NullValue: storagepb.NullValue_NULL_VALUE_NULL,
					}},
				})
				continue
			}
			number, ok := asFloat64(value)
			if !ok {
				return 0, fmt.Errorf("factor output %s returned non-numeric value %T", name, value)
			}
			row.Fields = append(row.Fields, doubleField(name, number))
		}
		rows = append(rows, row)
	}
	rsp, err := c.access.UpsertFields(ctx, &storagepb.PrimaryUpsertFieldsReq{AuthInfo: c.auth, Rows: rows})
	if err != nil {
		return 0, fmt.Errorf("write factor patch: %w", err)
	}
	if err := ensureStorageOK("write factor patch", rsp.GetRetInfo()); err != nil {
		return 0, err
	}
	return uint64(len(rows)), nil
}

func stringValue(value string) *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}}
}

func asFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	default:
		return 0, false
	}
}
