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
	targetTimes []time.Time,
	result *engine.FactorResult,
) (uint64, error) {
	if task == nil || result == nil {
		return 0, fmt.Errorf("task and result are required")
	}
	rows := make([]*storagepb.RowFieldUpsert, 0, len(targetTimes))
	computedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for i, targetTime := range targetTimes {
		row := &storagepb.RowFieldUpsert{
			Key: &storagepb.RowKey{
				SpaceId: task.SpaceID, DatasetId: task.TargetDataset,
				Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
					SubjectId: task.SubjectID, Freq: task.Freq,
					DataTime: targetTime.UTC().Format(time.RFC3339Nano),
				}},
			},
			Attributes: map[string]*storagepb.TypedValue{
				"factor.parent_task_id": stringValue(task.TaskID),
				"factor.computed_at":    stringValue(computedAt),
			},
		}
		for name, values := range result.Columns {
			if i >= len(values) || values[i] == nil {
				continue
			}
			value, ok := asFloat64(values[i])
			if !ok {
				return 0, fmt.Errorf("factor column %s returned non-numeric value %T", name, values[i])
			}
			row.Fields = append(row.Fields, doubleField(name, value))
		}
		if len(row.Fields) > 0 {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return 0, nil
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
