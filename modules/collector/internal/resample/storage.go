package resample

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

var ErrResampleSourceIncomplete = errors.New("resample source window incomplete")

const (
	writeSource      = "collector:kline_resample"
	fieldOpen        = "open"
	fieldHigh        = "high"
	fieldLow         = "low"
	fieldClose       = "close"
	fieldVolume      = "volume"
	fieldQuoteVolume = "quote_volume"
	fieldTradeNum    = "trade_num"
)

// PrimaryStorage is the exact-read and idempotent-write surface required by a
// local resample worker. The interface is intentionally small for deterministic
// unit tests and to keep the remote adapter independent of control-plane state.
type PrimaryStorage interface {
	ReadFields(context.Context, []*storagepb.RowKey, []string, []string) ([]*storagepb.RowFieldValues, error)
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
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
type BucketStorage struct {
	Primary PrimaryStorage
}

// ProcessBucket performs one complete target bucket operation.
func (s BucketStorage) ProcessBucket(ctx context.Context, spec RuleSpec, subjectID string, start, end time.Time) (Result, bool, error) {
	if s.Primary == nil {
		return Result{}, false, fmt.Errorf("resample Primary storage is required")
	}
	times, err := ExpectedSourceTimes(start, end, spec.SourceFrequency)
	if err != nil {
		return Result{}, false, err
	}
	keys := make([]*storagepb.RowKey, 0, len(times))
	for _, at := range times {
		keys = append(keys, rowKey(spec.SpaceID, spec.SourceDatasetID, subjectID, spec.SourceFrequency.Storage, at, spec.SourceSeriesTag))
	}
	rows, err := s.Primary.ReadFields(ctx, keys, []string{fieldOpen, fieldHigh, fieldLow, fieldClose, fieldVolume, fieldQuoteVolume, fieldTradeNum}, nil)
	if err != nil {
		return Result{}, false, err
	}
	sourceBars := make([]SourceBar, 0, len(rows))
	for _, row := range rows {
		bar, decodeErr := decodeSourceBar(row)
		if decodeErr != nil {
			// A malformed or incomplete source row is equivalent to a missing
			// source bar for retry/retention policy. Keep Storage transport and
			// target write errors outside this class so they remain transient.
			return Result{}, false, fmt.Errorf("%w: %v", ErrResampleSourceIncomplete, decodeErr)
		}
		sourceBars = append(sourceBars, bar)
	}
	result, err := Bars(spec, subjectID, start, end, sourceBars)
	if err != nil {
		return Result{}, false, fmt.Errorf("%w: %v", ErrResampleSourceIncomplete, err)
	}
	targetKey := rowKey(spec.SpaceID, spec.TargetDatasetID, subjectID, spec.TargetFrequency.Storage, result.DataTime, spec.SourceSeriesTag)
	// PrimaryStore requires at least one field ID even when only an attribute
	// is needed. Request the canonical K-line fields alongside source_hash;
	// this remains a single bounded read and keeps the idempotency check valid.
	existing, err := s.Primary.ReadFields(ctx, []*storagepb.RowKey{targetKey}, []string{fieldOpen, fieldHigh, fieldLow, fieldClose, fieldVolume, fieldQuoteVolume, fieldTradeNum}, []string{"source_hash"})
	if err != nil {
		return Result{}, false, err
	}
	if len(existing) > 0 && typedString(existing[0].GetAttributes()["source_hash"]) == result.SourceHash {
		return result, false, nil
	}
	row := resultRowWithSpec(result, spec)
	eventID := sourceEventID(spec.RuleID, targetKey, result.SourceHash)
	if err := s.Primary.UpsertFieldsWithSource(ctx, []*storagepb.RowFieldUpsert{row}, eventID); err != nil {
		return Result{}, false, err
	}
	return result, true, nil
}

func rowKey(spaceID, datasetID, subjectID, frequency string, at time.Time, seriesTag string) *storagepb.RowKey {
	return &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
		SubjectId: subjectID, Freq: frequency, DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: seriesTag,
	}}}
}

func resultRow(result Result) *storagepb.RowFieldUpsert {
	return &storagepb.RowFieldUpsert{
		Key: rowKey(result.SpaceID, result.DatasetID, result.SubjectID, result.Frequency, result.DataTime, result.SeriesTag),
		Fields: []*storagepb.FieldValue{
			doubleField(fieldOpen, result.Open), doubleField(fieldHigh, result.High), doubleField(fieldLow, result.Low),
			doubleField(fieldClose, result.Close), doubleField(fieldVolume, result.Volume), doubleField(fieldQuoteVolume, result.QuoteVolume), intField(fieldTradeNum, result.TradeNum),
		},
		Attributes: map[string]*storagepb.TypedValue{
			"resample_rule_id":  stringValue(""),
			"source_dataset_id": stringValue(""),
			"source_freq":       stringValue(""),
			"source_window_end": stringValue(result.SourceWindowEnd.UTC().Format(time.RFC3339Nano)),
			"source_hash":       stringValue(result.SourceHash),
		},
	}
}

func resultRowWithSpec(result Result, spec RuleSpec) *storagepb.RowFieldUpsert {
	row := resultRow(result)
	row.Attributes["resample_rule_id"] = stringValue(spec.RuleID)
	row.Attributes["source_dataset_id"] = stringValue(spec.SourceDatasetID)
	row.Attributes["source_freq"] = stringValue(spec.SourceFrequency.Storage)
	return row
}

func sourceEventID(ruleID string, key *storagepb.RowKey, hash string) string {
	keyText := ""
	if key != nil && key.GetTimeSeries() != nil {
		k := key.GetTimeSeries()
		keyText = strings.Join([]string{key.GetSpaceId(), key.GetDatasetId(), k.GetSubjectId(), k.GetFreq(), k.GetDataTime(), k.GetSeriesTag()}, "\x00")
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{ruleID, keyText, hash}, "\x00")))
	return "resample-" + hex.EncodeToString(sum[:])[:32]
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
	quote, err := requiredDouble(row, fieldQuoteVolume)
	if err != nil {
		return SourceBar{}, err
	}
	trade, err := requiredInt(row, fieldTradeNum)
	if err != nil {
		return SourceBar{}, err
	}
	return SourceBar{SpaceID: row.GetKey().GetSpaceId(), DatasetID: row.GetKey().GetDatasetId(), SubjectID: k.GetSubjectId(), Frequency: k.GetFreq(), DataTime: at.UTC(), SeriesTag: k.GetSeriesTag(), Open: &open, High: &high, Low: &low, Close: &closeValue, Volume: &volume, QuoteVolume: &quote, TradeNum: &trade}, nil
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
		if field != nil && field.GetFieldId() == name {
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
