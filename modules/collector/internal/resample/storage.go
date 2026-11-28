package resample

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

var ErrResampleSourceIncomplete = errors.New("resample source window incomplete")

const (
	fieldOpen        = "open"
	fieldHigh        = "high"
	fieldLow         = "low"
	fieldClose       = "close"
	fieldVolume      = "volume"
	fieldQuoteVolume = "quote_volume"
	fieldTradeNum    = "trade_num"
	fieldAmount      = "amount"
)

// sourceFields 按列规则表返回需要从源数据集读取的列名。
func sourceFields(rules []FieldRule) []string {
	fields := make([]string, len(rules))
	for i, rule := range rules {
		fields[i] = rule.Name
	}
	return fields
}

// PrimaryStorage is the exact-read and write surface required by a local
// resample worker; rewriting the same bucket simply overwrites it. The interface is intentionally small for deterministic
// unit tests and to keep the remote adapter independent of control-plane state.
type PrimaryStorage interface {
	ReadRows(context.Context, []*storagepb.RowKey, []string, []string) ([]*storagepb.Row, error)
	UpsertFields(context.Context, []*storagepb.Row) error
}

// ViewWaiter 是合成用到的 DataView 方法：建目标时等查询索引的列建好。
type ViewWaiter interface {
	GetViewState(context.Context, *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error)
}

// viewRetryInterval 是查询索引还没准备好（还没开始建索引、正在建索引）时再问一次的间隔。
const viewRetryInterval = 100 * time.Millisecond

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func rowKey(spaceID, datasetID, subjectID, frequency string, at time.Time, seriesTag string) *storagepb.RowKey {
	return &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
		SubjectId: subjectID, Frequency: frequency, DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: seriesTag,
	}}}
}

func resultRowWithSpec(result Result, spec TaskSpec) *storagepb.Row {
	fields := make([]*storagepb.FieldValue, 0, len(spec.Rules))
	for _, rule := range spec.Rules {
		val, ok := result.Values[rule.Name]
		if !ok || val.Null {
			fields = append(fields, nullField(rule.Name))
			continue
		}
		switch rule.ValueType {
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
			fields = append(fields, intField(rule.Name, val.Int))
		default:
			fields = append(fields, doubleField(rule.Name, val.Float))
		}
	}
	return &storagepb.Row{
		Key:    rowKey(result.SpaceID, result.DatasetID, result.SubjectID, result.Frequency, result.DataTime, result.SeriesTag),
		Fields: fields,
		Attributes: map[string]*storagepb.TypedValue{
			"resample_task_id":  stringValue(spec.TaskID),
			"source_dataset_id": stringValue(spec.SourceDatasetID),
			"source_freq":       stringValue(spec.SourceFrequency.Storage),
			"source_window_end": stringValue(result.SourceWindowEnd.UTC().Format(time.RFC3339Nano)),
			"source_hash":       stringValue(result.SourceHash),
		},
	}
}

func decodeSourceBar(row *storagepb.Row, rules []FieldRule) (SourceBar, error) {
	if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
		return SourceBar{}, fmt.Errorf("source row key is invalid")
	}
	k := row.GetKey().GetTimeSeries()
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(k.GetDataTime()))
	if err != nil {
		return SourceBar{}, fmt.Errorf("source row data_time: %w", err)
	}
	values := make(map[string]FieldValue, len(rules))
	for _, rule := range rules {
		switch rule.ValueType {
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
			val, decodeErr := decodeDoubleField(row, rule)
			if decodeErr != nil {
				return SourceBar{}, decodeErr
			}
			values[rule.Name] = val
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
			val, decodeErr := decodeIntField(row, rule)
			if decodeErr != nil {
				return SourceBar{}, decodeErr
			}
			values[rule.Name] = val
		default:
			return SourceBar{}, fmt.Errorf("unsupported column type %s for field %q", rule.ValueType.String(), rule.Name)
		}
	}
	return SourceBar{
		SpaceID:   row.GetKey().GetSpaceId(),
		DatasetID: row.GetKey().GetDatasetId(),
		SubjectID: k.GetSubjectId(),
		Frequency: k.GetFrequency(),
		DataTime:  at.UTC(),
		SeriesTag: k.GetSeriesTag(),
		Values:    values,
	}, nil
}

func decodeDoubleField(row *storagepb.Row, rule FieldRule) (FieldValue, error) {
	value, ok := fieldValue(row, rule.Name)
	if !ok || value == nil {
		if rule.Nullable {
			return FieldValue{Null: true}, nil
		}
		return FieldValue{}, fmt.Errorf("source row missing field %q", rule.Name)
	}
	if _, isNull := value.GetValue().(*storagepb.TypedValue_NullValue); isNull {
		if rule.Nullable {
			return FieldValue{Null: true}, nil
		}
		return FieldValue{}, fmt.Errorf("source row missing field %q", rule.Name)
	}
	if _, isDouble := value.GetValue().(*storagepb.TypedValue_DoubleValue); !isDouble {
		return FieldValue{}, fmt.Errorf("source field %q is not double", rule.Name)
	}
	v := value.GetDoubleValue()
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return FieldValue{}, fmt.Errorf("source field %q is not finite", rule.Name)
	}
	return FieldValue{Float: v}, nil
}

func decodeIntField(row *storagepb.Row, rule FieldRule) (FieldValue, error) {
	value, ok := fieldValue(row, rule.Name)
	if !ok || value == nil {
		if rule.Nullable {
			return FieldValue{Null: true}, nil
		}
		return FieldValue{}, fmt.Errorf("source row missing field %q", rule.Name)
	}
	if _, isNull := value.GetValue().(*storagepb.TypedValue_NullValue); isNull {
		if rule.Nullable {
			return FieldValue{Null: true}, nil
		}
		return FieldValue{}, fmt.Errorf("source row missing field %q", rule.Name)
	}
	if _, isInt := value.GetValue().(*storagepb.TypedValue_IntValue); !isInt {
		return FieldValue{}, fmt.Errorf("source field %q is not int", rule.Name)
	}
	return FieldValue{Int: value.GetIntValue()}, nil
}

func fieldValue(row *storagepb.Row, name string) (*storagepb.TypedValue, bool) {
	for _, field := range row.GetFields() {
		if field == nil {
			continue
		}
		fieldID := strings.TrimSpace(field.GetFieldId())
		if fieldID == name || strings.HasSuffix(fieldID, "."+name) {
			return field.GetValue(), true
		}
	}
	return nil, false
}

func stringValue(value string) *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}}
}

func doubleField(name string, value float64) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func intField(name string, value int64) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: value}}}
}

func nullField(name string) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{NullValue: storagepb.NullValue_NULL_VALUE_NULL}}}
}
