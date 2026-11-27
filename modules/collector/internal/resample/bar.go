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

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
	// Rules 是本次重采样实际读取、归约与写入的列规则（标准归约规则表 ∩ 源数据集活跃列 ∩ OutputFields）。
	Rules []FieldRule
}

// SourceBar 是一根已解码的源 K 线；列值按列规则表动态存放，不含市场专属字段。
type SourceBar struct {
	SpaceID   string
	DatasetID string
	SubjectID string
	Frequency string
	DataTime  time.Time
	SeriesTag string
	Values    map[string]FieldValue
}

// Result 是一根完整的目标 K 线与源窗口内容指纹。
type Result struct {
	SpaceID         string
	DatasetID       string
	SubjectID       string
	Frequency       string
	DataTime        time.Time
	SeriesTag       string
	Values          map[string]FieldValue
	SourceWindowEnd time.Time
	SourceHash      string
}

// Aggregate 把一个桶内全部应有的源 K 线按 spec.Rules 声明的归约算子聚合成一根目标 K 线。
// expected 是桶内应有的源 K 线开盘时刻（升序，由 barclock 给出）；rows 必须与它一一对应，少一根都不输出半根。
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

	for index := range ordered {
		if err := validateSourceBar(spec, subjectID, expected[index], &ordered[index]); err != nil {
			return Result{}, fmt.Errorf("source row %d: %w", index, err)
		}
	}

	values := make(map[string]FieldValue, len(spec.Rules))
	for _, rule := range spec.Rules {
		reduced, err := reduceRule(rule, ordered)
		if err != nil {
			return Result{}, err
		}
		values[rule.Name] = reduced
	}

	return Result{
		SpaceID:         spec.SpaceID,
		DatasetID:       spec.TargetDatasetID,
		SubjectID:       subjectID,
		Frequency:       spec.TargetFrequency.Storage,
		DataTime:        bucket.Key.UTC(),
		SeriesTag:       spec.SourceSeriesTag,
		Values:          values,
		SourceWindowEnd: bucket.End.UTC(),
		SourceHash:      hashSourceBars(ordered, spec.Rules),
	}, nil
}

func reduceRule(rule FieldRule, ordered []SourceBar) (FieldValue, error) {
	switch rule.Op {
	case ReduceFirst:
		return ordered[0].Values[rule.Name], nil
	case ReduceLast:
		return ordered[len(ordered)-1].Values[rule.Name], nil
	case ReduceMax:
		best := ordered[0].Values[rule.Name].Float
		for i := 1; i < len(ordered); i++ {
			best = math.Max(best, ordered[i].Values[rule.Name].Float)
		}
		return FieldValue{Float: best}, nil
	case ReduceMin:
		best := ordered[0].Values[rule.Name].Float
		for i := 1; i < len(ordered); i++ {
			best = math.Min(best, ordered[i].Values[rule.Name].Float)
		}
		return FieldValue{Float: best}, nil
	case ReduceSum:
		sum := 0.0
		for i := range ordered {
			sum += ordered[i].Values[rule.Name].Float
			if !finite(sum) {
				return FieldValue{}, fmt.Errorf("source row %d: %s sum is not finite", i, rule.Name)
			}
		}
		return FieldValue{Float: sum}, nil
	case ReduceSumAllPresent:
		sum := 0.0
		for i := range ordered {
			val, ok := ordered[i].Values[rule.Name]
			if !ok || val.Null {
				return FieldValue{Null: true}, nil
			}
			sum += val.Float
			if !finite(sum) {
				return FieldValue{}, fmt.Errorf("source row %d: %s sum is not finite", i, rule.Name)
			}
		}
		return FieldValue{Float: sum}, nil
	case ReduceSumInt:
		var sum int64
		for i := range ordered {
			v := ordered[i].Values[rule.Name].Int
			if v > 0 && sum > math.MaxInt64-v {
				return FieldValue{}, fmt.Errorf("source row %d: %s sum overflows int64", i, rule.Name)
			}
			if v < 0 && sum < math.MinInt64-v {
				return FieldValue{}, fmt.Errorf("source row %d: %s sum underflows int64", i, rule.Name)
			}
			sum += v
		}
		return FieldValue{Int: sum}, nil
	default:
		return FieldValue{}, fmt.Errorf("unsupported reduce op %q for field %s", rule.Op, rule.Name)
	}
}

func validateTaskSpec(spec TaskSpec) error {
	if spec.TaskID == "" || spec.SpaceID == "" || spec.SourceDatasetID == "" || spec.SourceSeriesTag == "" || spec.TargetDatasetID == "" {
		return fmt.Errorf("resample task identity, source, target, and series tag are required")
	}
	if spec.SourceDatasetID == spec.TargetDatasetID {
		return fmt.Errorf("source and target Dataset IDs must differ")
	}
	if len(spec.Rules) == 0 {
		return fmt.Errorf("resample task requires at least one field rule")
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
	for _, rule := range spec.Rules {
		val, ok := row.Values[rule.Name]
		if !ok || val.Null {
			if rule.Nullable {
				continue
			}
			return fmt.Errorf("required field %s is missing", rule.Name)
		}
		switch rule.ValueType {
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
			if !finite(val.Float) {
				return fmt.Errorf("field %s must be finite", rule.Name)
			}
			if rule.NonNegative && val.Float < 0 {
				return fmt.Errorf("field %s must not be negative", rule.Name)
			}
		case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
			if rule.NonNegative && val.Int < 0 {
				return fmt.Errorf("field %s must not be negative", rule.Name)
			}
		}
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func hashSourceBars(rows []SourceBar, rules []FieldRule) string {
	digest := sha256.New()
	writeHashString(digest, "moox:kline-resample-source:v2")
	for index := range rows {
		row := &rows[index]
		writeHashString(digest, row.SpaceID)
		writeHashString(digest, row.DatasetID)
		writeHashString(digest, row.SubjectID)
		writeHashString(digest, row.Frequency)
		writeHashInt64(digest, row.DataTime.UTC().UnixNano())
		writeHashString(digest, row.SeriesTag)
		for _, rule := range rules {
			val, ok := row.Values[rule.Name]
			if !ok || val.Null {
				writeHashString(digest, rule.Name+":null")
				continue
			}
			switch rule.ValueType {
			case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
				writeHashString(digest, rule.Name)
				writeHashInt64(digest, val.Int)
			default:
				writeHashFloat64(digest, rule.Name, val.Float)
			}
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
