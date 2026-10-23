package resample

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"sort"
	"time"

	"github.com/mooyang-code/moox/packages/barclock"
)

// TaskSpec 是聚合所需的不可变输入输出契约。
type TaskSpec struct {
	TaskID          string
	SpaceID         string
	SourceDatasetID string
	SourceFrequency FixedFrequency
	SourceSeriesTag string
	TargetDatasetID string
	TargetFrequency FixedFrequency
	OutputFields    []string
}

// SourceBar is one decoded source K-line. Pointer fields preserve whether a
// required Storage field was absent.
type SourceBar struct {
	SpaceID     string
	DatasetID   string
	SubjectID   string
	Frequency   string
	DataTime    time.Time
	SeriesTag   string
	Open        *float64
	High        *float64
	Low         *float64
	Close       *float64
	Volume      *float64
	QuoteVolume *float64
	TradeNum    *int64
	// Amount 是 A 股的成交额；采集拿不到真实成交额时为空。
	Amount *float64
}

// Result is one complete target K-line plus the deterministic source-window hash.
type Result struct {
	SpaceID     string
	DatasetID   string
	SubjectID   string
	Frequency   string
	DataTime    time.Time
	SeriesTag   string
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Volume      float64
	QuoteVolume float64
	TradeNum    int64
	// Amount 是 A 股的成交额：桶内有任何一根源 K 线没有真实成交额时为空（写入 NULL）。
	Amount          *float64
	SourceWindowEnd time.Time
	SourceHash      string
}

// Aggregate 把一个桶内全部应有的源 K 线聚合成一根目标 K 线。
// expected 是桶内应有的源 K 线开盘时刻（升序，由 barclock 按市场的交易时段给出）；rows 必须与它一一对应，少一根都不输出半根。
func Aggregate(spec TaskSpec, subjectID string, bucket barclock.Bucket, expected []time.Time, rows []SourceBar) (Result, error) {
	if err := validateTaskSpec(spec); err != nil {
		return Result{}, err
	}
	if subjectID == "" {
		return Result{}, fmt.Errorf("subject ID is required")
	}
	if len(expected) == 0 {
		return Result{}, fmt.Errorf("bucket %s has no expected source bars", bucket.Key.Format(time.RFC3339))
	}
	if len(rows) != len(expected) {
		return Result{}, fmt.Errorf("source window requires %d rows, got %d", len(expected), len(rows))
	}

	ordered := append([]SourceBar(nil), rows...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].DataTime.Before(ordered[j].DataTime)
	})

	var result Result
	crypto := hasTradeCount(spec.SpaceID)
	amount, amountKnown := 0.0, true
	for index := range ordered {
		row := &ordered[index]
		if err := validateSourceBar(spec, subjectID, expected[index], row); err != nil {
			return Result{}, fmt.Errorf("source row %d: %w", index, err)
		}
		if index == 0 {
			result.Open = *row.Open
			result.High = *row.High
			result.Low = *row.Low
		} else {
			result.High = math.Max(result.High, *row.High)
			result.Low = math.Min(result.Low, *row.Low)
		}
		result.Close = *row.Close
		result.Volume += *row.Volume
		if crypto {
			result.QuoteVolume += *row.QuoteVolume
			if *row.TradeNum > math.MaxInt64-result.TradeNum {
				return Result{}, fmt.Errorf("source row %d: trade_num sum overflows int64", index)
			}
			result.TradeNum += *row.TradeNum
		} else if row.Amount == nil {
			amountKnown = false
		} else {
			amount += *row.Amount
		}
		if !finite(result.Volume) || !finite(result.QuoteVolume) || !finite(amount) {
			return Result{}, fmt.Errorf("source row %d: volume sum is not finite", index)
		}
	}
	if !crypto && amountKnown {
		result.Amount = &amount
	}

	result.SpaceID = spec.SpaceID
	result.DatasetID = spec.TargetDatasetID
	result.SubjectID = subjectID
	result.Frequency = spec.TargetFrequency.Storage
	result.DataTime = bucket.Key.UTC()
	result.SeriesTag = spec.SourceSeriesTag
	result.SourceWindowEnd = bucket.End.UTC()
	result.SourceHash = hashSourceBars(ordered)
	return result, nil
}

func validateTaskSpec(spec TaskSpec) error {
	if spec.TaskID == "" || spec.SpaceID == "" || spec.SourceDatasetID == "" || spec.SourceSeriesTag == "" || spec.TargetDatasetID == "" {
		return fmt.Errorf("resample task identity, source, target, and series tag are required")
	}
	if spec.SourceDatasetID == spec.TargetDatasetID {
		return fmt.Errorf("source and target Dataset IDs must differ")
	}
	if err := ValidateResamplePair(spec.SourceFrequency, spec.TargetFrequency); err != nil {
		return err
	}
	return ValidateTargetDatasetID(spec.TargetDatasetID)
}

func validateSourceBar(spec TaskSpec, subjectID string, expectedTime time.Time, row *SourceBar) error {
	if row.SpaceID != spec.SpaceID || row.DatasetID != spec.SourceDatasetID {
		return fmt.Errorf("source Dataset key does not match task")
	}
	if row.SubjectID != subjectID {
		return fmt.Errorf("source subject does not match claim")
	}
	if row.Frequency != spec.SourceFrequency.Storage {
		return fmt.Errorf("source frequency does not match task")
	}
	if row.SeriesTag != spec.SourceSeriesTag {
		return fmt.Errorf("source series tag does not match task")
	}
	if !row.DataTime.UTC().Equal(expectedTime) {
		return fmt.Errorf("expected data_time %s, got %s", expectedTime.Format(time.RFC3339Nano), row.DataTime.Format(time.RFC3339Nano))
	}
	values := []struct {
		name  string
		value *float64
	}{
		{name: "open", value: row.Open},
		{name: "high", value: row.High},
		{name: "low", value: row.Low},
		{name: "close", value: row.Close},
		{name: "volume", value: row.Volume},
	}
	crypto := hasTradeCount(spec.SpaceID)
	if crypto {
		values = append(values, struct {
			name  string
			value *float64
		}{name: "quote_volume", value: row.QuoteVolume})
	}
	for _, field := range values {
		if field.value == nil {
			return fmt.Errorf("required field %s is missing", field.name)
		}
		if !finite(*field.value) {
			return fmt.Errorf("field %s must be finite", field.name)
		}
	}
	if *row.Volume < 0 || (crypto && *row.QuoteVolume < 0) {
		return fmt.Errorf("volume fields must not be negative")
	}
	if !crypto {
		if row.Amount != nil && (!finite(*row.Amount) || *row.Amount < 0) {
			return fmt.Errorf("amount must be finite and not negative")
		}
		return nil
	}
	if row.TradeNum == nil {
		return fmt.Errorf("required field trade_num is missing")
	}
	if *row.TradeNum < 0 {
		return fmt.Errorf("trade_num must not be negative")
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func hashSourceBars(rows []SourceBar) string {
	digest := sha256.New()
	writeHashString(digest, "moox:kline-resample-source:v1")
	for index := range rows {
		row := &rows[index]
		writeHashString(digest, row.SpaceID)
		writeHashString(digest, row.DatasetID)
		writeHashString(digest, row.SubjectID)
		writeHashString(digest, row.Frequency)
		writeHashInt64(digest, row.DataTime.UTC().UnixNano())
		writeHashString(digest, row.SeriesTag)
		writeHashFloat64(digest, "open", *row.Open)
		writeHashFloat64(digest, "high", *row.High)
		writeHashFloat64(digest, "low", *row.Low)
		writeHashFloat64(digest, "close", *row.Close)
		writeHashFloat64(digest, "volume", *row.Volume)
		if row.TradeNum != nil && row.QuoteVolume != nil {
			writeHashFloat64(digest, "quote_volume", *row.QuoteVolume)
			writeHashString(digest, "trade_num")
			writeHashInt64(digest, *row.TradeNum)
		} else if row.Amount != nil {
			writeHashFloat64(digest, "amount", *row.Amount)
		} else {
			writeHashString(digest, "amount_unknown")
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeHashString(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

func writeHashInt64(digest hash.Hash, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, _ = digest.Write(encoded[:])
}

func writeHashFloat64(digest hash.Hash, field string, value float64) {
	writeHashString(digest, field)
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], math.Float64bits(value))
	_, _ = digest.Write(encoded[:])
}
