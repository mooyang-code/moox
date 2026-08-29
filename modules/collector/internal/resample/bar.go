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
)

const AlignmentEpochUTC = "epoch_utc"

// RuleSpec is the immutable input/output contract required by Bars.
type RuleSpec struct {
	RuleID          string
	SpaceID         string
	SourceDatasetID string
	SourceFrequency FixedFrequency
	SourceSeriesTag string
	TargetDatasetID string
	TargetFrequency FixedFrequency
	Alignment       string
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
}

// Result is one complete target K-line plus the deterministic source-window hash.
type Result struct {
	SpaceID         string
	DatasetID       string
	SubjectID       string
	Frequency       string
	DataTime        time.Time
	SeriesTag       string
	Open            float64
	High            float64
	Low             float64
	Close           float64
	Volume          float64
	QuoteVolume     float64
	TradeNum        int64
	SourceWindowEnd time.Time
	SourceHash      string
}

// ExpectedSourceTimes returns the exact source RowKey timestamps for [start, end).
func ExpectedSourceTimes(start, end time.Time, source FixedFrequency) ([]time.Time, error) {
	if err := validateFixedFrequency(source); err != nil {
		return nil, fmt.Errorf("source frequency: %w", err)
	}
	start = start.UTC()
	end = end.UTC()
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return nil, fmt.Errorf("source window must be non-empty")
	}
	if !start.Equal(start.Truncate(time.Minute)) || !end.Equal(end.Truncate(time.Minute)) {
		return nil, fmt.Errorf("source window must use whole-minute UTC boundaries")
	}
	duration := end.Sub(start)
	if duration%source.Duration != 0 {
		return nil, fmt.Errorf("source window must contain whole source periods")
	}
	count := int(duration / source.Duration)
	if count <= 0 || count > maxSourceBars {
		return nil, fmt.Errorf("source window must contain between 1 and %d rows", maxSourceBars)
	}
	times := make([]time.Time, count)
	for index := range times {
		times[index] = start.Add(time.Duration(index) * source.Duration)
	}
	return times, nil
}

// Bars validates and aggregates one complete source window into one target K-line.
func Bars(spec RuleSpec, subjectID string, start, end time.Time, rows []SourceBar) (Result, error) {
	if err := validateRuleSpec(spec); err != nil {
		return Result{}, err
	}
	if subjectID == "" {
		return Result{}, fmt.Errorf("subject ID is required")
	}
	start = start.UTC()
	end = end.UTC()
	if end.Sub(start) != spec.TargetFrequency.Duration {
		return Result{}, fmt.Errorf("source window must equal one target bucket")
	}
	bucketStart, bucketEnd := BucketAt(end, time.Unix(0, 0).UTC(), spec.TargetFrequency)
	if !bucketStart.Equal(start) || !bucketEnd.Equal(end) {
		return Result{}, fmt.Errorf("source window must be epoch UTC aligned")
	}
	expected, err := ExpectedSourceTimes(start, end, spec.SourceFrequency)
	if err != nil {
		return Result{}, err
	}
	if len(rows) != len(expected) {
		return Result{}, fmt.Errorf("source window requires %d rows, got %d", len(expected), len(rows))
	}

	ordered := append([]SourceBar(nil), rows...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].DataTime.Before(ordered[j].DataTime)
	})

	var result Result
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
		result.QuoteVolume += *row.QuoteVolume
		if *row.TradeNum > math.MaxInt64-result.TradeNum {
			return Result{}, fmt.Errorf("source row %d: trade_num sum overflows int64", index)
		}
		result.TradeNum += *row.TradeNum
		if !finite(result.Volume) || !finite(result.QuoteVolume) {
			return Result{}, fmt.Errorf("source row %d: volume sum is not finite", index)
		}
	}

	result.SpaceID = spec.SpaceID
	result.DatasetID = spec.TargetDatasetID
	result.SubjectID = subjectID
	result.Frequency = spec.TargetFrequency.Storage
	result.DataTime = start
	result.SeriesTag = spec.SourceSeriesTag
	result.SourceWindowEnd = end
	result.SourceHash = hashSourceBars(ordered)
	return result, nil
}

func validateRuleSpec(spec RuleSpec) error {
	if spec.RuleID == "" || spec.SpaceID == "" || spec.SourceDatasetID == "" || spec.SourceSeriesTag == "" || spec.TargetDatasetID == "" {
		return fmt.Errorf("resample rule identity, source, target, and series tag are required")
	}
	if spec.SourceDatasetID == spec.TargetDatasetID {
		return fmt.Errorf("source and target Dataset IDs must differ")
	}
	if spec.Alignment != AlignmentEpochUTC {
		return fmt.Errorf("alignment must be %s", AlignmentEpochUTC)
	}
	if err := ValidateResamplePair(spec.SourceFrequency, spec.TargetFrequency); err != nil {
		return err
	}
	if err := ValidateTargetDatasetID(spec.TargetDatasetID, spec.TargetFrequency.Slug); err != nil {
		return err
	}
	return nil
}

func validateSourceBar(spec RuleSpec, subjectID string, expectedTime time.Time, row *SourceBar) error {
	if row.SpaceID != spec.SpaceID || row.DatasetID != spec.SourceDatasetID {
		return fmt.Errorf("source Dataset key does not match rule")
	}
	if row.SubjectID != subjectID {
		return fmt.Errorf("source subject does not match claim")
	}
	if row.Frequency != spec.SourceFrequency.Storage {
		return fmt.Errorf("source frequency does not match rule")
	}
	if row.SeriesTag != spec.SourceSeriesTag {
		return fmt.Errorf("source series tag does not match rule")
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
		{name: "quote_volume", value: row.QuoteVolume},
	}
	for _, field := range values {
		if field.value == nil {
			return fmt.Errorf("required field %s is missing", field.name)
		}
		if !finite(*field.value) {
			return fmt.Errorf("field %s must be finite", field.name)
		}
	}
	if *row.Volume < 0 || *row.QuoteVolume < 0 {
		return fmt.Errorf("volume fields must not be negative")
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
		writeHashFloat64(digest, "quote_volume", *row.QuoteVolume)
		writeHashString(digest, "trade_num")
		writeHashInt64(digest, *row.TradeNum)
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
