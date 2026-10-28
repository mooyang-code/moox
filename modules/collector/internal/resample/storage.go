package resample

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
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

// hasTradeCount 判断市场的 K 线是否带报价成交量和成交笔数（加密货币）；A 股只有成交额 amount。
func hasTradeCount(spaceID string) bool {
	return !strings.EqualFold(strings.TrimSpace(spaceID), "stockcn")
}

// sourceFields 是读取源 K 线的字段。
func sourceFields(spaceID string) []string {
	fields := []string{fieldOpen, fieldHigh, fieldLow, fieldClose, fieldVolume}
	if hasTradeCount(spaceID) {
		return append(fields, fieldQuoteVolume, fieldTradeNum)
	}
	return append(fields, fieldAmount)
}

// PrimaryStorage is the exact-read and write surface required by a local
// resample worker; rewriting the same bucket simply overwrites it. The interface is intentionally small for deterministic
// unit tests and to keep the remote adapter independent of control-plane state.
type PrimaryStorage interface {
	ReadRows(context.Context, []*storagepb.RowKey, []string, []string) ([]*storagepb.RowFieldValues, error)
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
}

// SyncPointStorage is the optional catch-up fence used after a historical
// resample. Primary rows are written first, then a stable catch-up marker is
// appended and the target View is waited on before the backfill is reported
// complete.
type SyncPointStorage interface {
	AppendDatasetSyncPoint(context.Context, string, string, string, string) error
	WaitViewSyncPoint(context.Context, *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error)
}

// BucketStorage reads one source bucket and writes its target row only when the
// source hash changed. It is safe to retry after a crash between write and CAS.
func rowKey(spaceID, datasetID, subjectID, frequency string, at time.Time, seriesTag string) *storagepb.RowKey {
	return &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
		SubjectId: subjectID, Freq: frequency, DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: seriesTag,
	}}}
}

func resultRow(result Result) *storagepb.RowFieldUpsert {
	fields := []*storagepb.FieldValue{
		doubleField(fieldOpen, result.Open), doubleField(fieldHigh, result.High), doubleField(fieldLow, result.Low),
		doubleField(fieldClose, result.Close), doubleField(fieldVolume, result.Volume),
	}
	switch {
	case hasTradeCount(result.SpaceID):
		fields = append(fields, doubleField(fieldQuoteVolume, result.QuoteVolume), intField(fieldTradeNum, result.TradeNum))
	case result.Amount != nil:
		fields = append(fields, doubleField(fieldAmount, *result.Amount))
	default:
		// 源 K 线没有真实成交额：显式写 NULL，覆盖该行之前可能写过的值。
		fields = append(fields, &storagepb.FieldValue{FieldId: fieldAmount, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{NullValue: storagepb.NullValue_NULL_VALUE_NULL}}})
	}
	return &storagepb.RowFieldUpsert{
		Key:    rowKey(result.SpaceID, result.DatasetID, result.SubjectID, result.Frequency, result.DataTime, result.SeriesTag),
		Fields: fields,
		Attributes: map[string]*storagepb.TypedValue{
			"resample_task_id":  stringValue(""),
			"source_dataset_id": stringValue(""),
			"source_freq":       stringValue(""),
			"source_window_end": stringValue(result.SourceWindowEnd.UTC().Format(time.RFC3339Nano)),
			"source_hash":       stringValue(result.SourceHash),
		},
	}
}

func resultRowWithSpec(result Result, spec TaskSpec) *storagepb.RowFieldUpsert {
	row := resultRow(result)
	if len(spec.OutputFields) > 0 {
		selected := make(map[string]struct{}, len(spec.OutputFields))
		for _, name := range spec.OutputFields {
			selected[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
		}
		fields := row.Fields[:0]
		for _, field := range row.Fields {
			if _, ok := selected[strings.ToLower(strings.TrimSpace(field.GetFieldId()))]; ok {
				fields = append(fields, field)
			}
		}
		row.Fields = fields
	}
	row.Attributes["resample_task_id"] = stringValue(spec.TaskID)
	row.Attributes["source_dataset_id"] = stringValue(spec.SourceDatasetID)
	row.Attributes["source_freq"] = stringValue(spec.SourceFrequency.Storage)
	return row
}

// attrOutputFields 记录目标行写入时的输出字段集合，参与幂等判断。
const attrOutputFields = "resample_output_fields"

// outputFieldsSignature 返回规范化（小写、去空白、排序、去重）后的输出字段集合；未限定输出字段时为 "*"。
func outputFieldsSignature(fields []string) string {
	if len(fields) == 0 {
		return "*"
	}
	seen := make(map[string]struct{}, len(fields))
	names := make([]string, 0, len(fields))
	for _, name := range fields {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return "*"
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func decodeSourceBar(row *storagepb.RowFieldValues) (SourceBar, error) {
	if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
		return SourceBar{}, fmt.Errorf("source row key is invalid")
	}
	k := row.GetKey().GetTimeSeries()
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(k.GetDataTime()))
	if err != nil {
		return SourceBar{}, fmt.Errorf("source row data_time: %w", err)
	}
	open, err := requiredDouble(row, fieldOpen)
	if err != nil {
		return SourceBar{}, err
	}
	high, err := requiredDouble(row, fieldHigh)
	if err != nil {
		return SourceBar{}, err
	}
	low, err := requiredDouble(row, fieldLow)
	if err != nil {
		return SourceBar{}, err
	}
	closeValue, err := requiredDouble(row, fieldClose)
	if err != nil {
		return SourceBar{}, err
	}
	volume, err := requiredDouble(row, fieldVolume)
	if err != nil {
		return SourceBar{}, err
	}
	bar := SourceBar{SpaceID: row.GetKey().GetSpaceId(), DatasetID: row.GetKey().GetDatasetId(), SubjectID: k.GetSubjectId(), Frequency: k.GetFreq(), DataTime: at.UTC(), SeriesTag: k.GetSeriesTag(), Open: &open, High: &high, Low: &low, Close: &closeValue, Volume: &volume}
	if hasTradeCount(bar.SpaceID) {
		quote, err := requiredDouble(row, fieldQuoteVolume)
		if err != nil {
			return SourceBar{}, err
		}
		trade, err := requiredInt(row, fieldTradeNum)
		if err != nil {
			return SourceBar{}, err
		}
		bar.QuoteVolume, bar.TradeNum = &quote, &trade
		return bar, nil
	}
	// A 股的成交额可以是 NULL（采集只拿到估算值时）：缺失表示未知，不算解码失败。
	if value, ok := fieldValue(row, fieldAmount); ok && value != nil {
		if _, isDouble := value.GetValue().(*storagepb.TypedValue_DoubleValue); isDouble {
			amount := value.GetDoubleValue()
			bar.Amount = &amount
		}
	}
	return bar, nil
}

func requiredDouble(row *storagepb.RowFieldValues, name string) (float64, error) {
	value, ok := fieldValue(row, name)
	if !ok || value == nil {
		return 0, fmt.Errorf("source row missing field %q", name)
	}
	if _, isDouble := value.GetValue().(*storagepb.TypedValue_DoubleValue); !isDouble {
		return 0, fmt.Errorf("source field %q is not double", name)
	}
	if math.IsNaN(value.GetDoubleValue()) || math.IsInf(value.GetDoubleValue(), 0) {
		return 0, fmt.Errorf("source field %q is not finite", name)
	}
	return value.GetDoubleValue(), nil
}

func requiredInt(row *storagepb.RowFieldValues, name string) (int64, error) {
	value, ok := fieldValue(row, name)
	if !ok || value == nil {
		return 0, fmt.Errorf("source row missing field %q", name)
	}
	if _, isInt := value.GetValue().(*storagepb.TypedValue_IntValue); !isInt {
		return 0, fmt.Errorf("source field %q is not int", name)
	}
	return value.GetIntValue(), nil
}

func fieldValue(row *storagepb.RowFieldValues, name string) (*storagepb.TypedValue, bool) {
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

func typedString(value *storagepb.TypedValue) string {
	if value == nil {
		return ""
	}
	return value.GetStringValue()
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
