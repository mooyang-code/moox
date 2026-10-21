// Package domain contains Collector control-plane domain types.
package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// CollectParams describes how a rule generates concrete task instances.
type CollectParams struct {
	// The persisted rule contract is flat and greenfield-only. The nested
	// fields below are derived for the collector's internal planners and are
	// deliberately excluded from JSON so old exchange/market aliases fail
	// validation rather than becoming a second public contract.
	Source          CollectSource   `json:"-"`
	Collector       CollectorSpec   `json:"-"`
	Target          CollectTarget   `json:"-"`
	Schedule        CollectSchedule `json:"-"`
	Provider        string          `json:"provider,omitempty"`
	MarketType      string          `json:"market_type,omitempty"`
	MarketID        string          `json:"market_id,omitempty"`
	InstrumentType  string          `json:"instrument_type,omitempty"`
	SourceID        string          `json:"source_id,omitempty"`
	SeriesTag       string          `json:"series_tag,omitempty"`
	SubjectTags     []string        `json:"subject_tags,omitempty"`
	OutputFields    []string        `json:"output_fields,omitempty"`
	TargetDatasetID string          `json:"target_dataset_id,omitempty"`
	Frequency       string          `json:"frequency,omitempty"`
	SourceDatasetID string          `json:"source_dataset_id,omitempty"`
	SourceFrequency string          `json:"source_frequency,omitempty"`
	SourceSeriesTag string          `json:"source_series_tag,omitempty"`
	TargetFrequency string          `json:"target_frequency,omitempty"`
	Alignment       string          `json:"alignment,omitempty"`
	SettleDelayMS   *int64          `json:"settle_delay_ms,omitempty"`
	// SymbolsPerFunction 是定时模式下每个函数负责的标的数；0 表示使用默认值。
	SymbolsPerFunction int `json:"symbols_per_function,omitempty"`
	// AllowedLatenessSeconds 是结果数据集的允许迟到时间；0 表示使用空间默认值（crypto 15 秒，stockcn 90 秒）。
	AllowedLatenessSeconds int `json:"allowed_lateness_seconds,omitempty"`
	// Ingest、Executor 是结果数据集采集方式的初值（stream / pull，scf / local）：数据集第一次出现时代替空间默认值写入，
	// 之后以 Collector 库为准，由 moox-cli collector ingest-mode set 切换。为空表示用空间默认值。
	Ingest   string `json:"ingest,omitempty"`
	Executor string `json:"executor,omitempty"`
}

// CollectSource describes where target objects come from.
type CollectSource struct {
	Kind      string `json:"kind"`
	DatasetID string `json:"dataset_id"`
}

// CollectorSpec describes the exchange/data adapter.
type CollectorSpec struct {
	Exchange  string   `json:"exchange"`
	Market    string   `json:"market"`
	DataType  string   `json:"data_type"`
	Intervals []string `json:"intervals"`
	Live      bool     `json:"live,omitempty"`
}

// CollectTarget describes where collected rows should be written.
type CollectTarget struct {
	DatasetID string `json:"dataset_id"`
}

// CollectSchedule describes task frequency dimensions.
type CollectSchedule struct {
	Interval string `json:"interval"`
}

// ParseCollectParams parses rule JSON and normalizes the standard shape.
func ParseCollectParams(raw string, fallbackProvider string, fallbackMarketType string, fallbackDataType string) (*CollectParams, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	var params CollectParams
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil {
		return nil, fmt.Errorf("parse collect params: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("parse collect params: trailing JSON value")
	}
	if err := validateCollectParamsShape(raw, fallbackDataType); err != nil {
		return nil, err
	}
	params.Normalize(fallbackProvider, fallbackMarketType, fallbackDataType)
	if err := params.validatePlanOptions(); err != nil {
		return nil, err
	}
	return &params, nil
}

func (p *CollectParams) validatePlanOptions() error {
	if p.SymbolsPerFunction < 0 || p.SymbolsPerFunction > 200 {
		return fmt.Errorf("parse collect params: symbols_per_function must be between 0 and 200")
	}
	if p.AllowedLatenessSeconds != 0 && (p.AllowedLatenessSeconds < 5 || p.AllowedLatenessSeconds > 3600) {
		return fmt.Errorf("parse collect params: allowed_lateness_seconds must be 0 (default) or between 5 and 3600")
	}
	p.Ingest, p.Executor = strings.ToLower(strings.TrimSpace(p.Ingest)), strings.ToLower(strings.TrimSpace(p.Executor))
	if p.Ingest != "" && p.Ingest != "stream" && p.Ingest != "pull" {
		return fmt.Errorf("parse collect params: ingest must be stream or pull")
	}
	if p.Executor != "" && p.Executor != "scf" && p.Executor != "local" {
		return fmt.Errorf("parse collect params: executor must be scf or local")
	}
	return nil
}

// Normalize canonicalizes the standard collect params shape.
func (p *CollectParams) Normalize(fallbackProvider string, fallbackMarketType string, fallbackDataType string) {
	p.Provider = strings.ToLower(firstNonEmpty(p.Provider, fallbackProvider))
	p.MarketType = strings.ToLower(firstNonEmpty(p.MarketType, fallbackMarketType))
	p.MarketID = strings.ToLower(strings.TrimSpace(p.MarketID))
	p.InstrumentType = strings.ToLower(strings.TrimSpace(p.InstrumentType))
	p.SourceID = strings.ToLower(strings.TrimSpace(p.SourceID))
	p.SeriesTag = strings.TrimSpace(p.SeriesTag)
	p.SubjectTags = normalizeTags(p.SubjectTags)
	p.OutputFields = normalizeOutputFields(p.OutputFields)
	p.TargetDatasetID = strings.TrimSpace(p.TargetDatasetID)
	p.Frequency = strings.TrimSpace(p.Frequency)
	p.SourceDatasetID = strings.TrimSpace(p.SourceDatasetID)
	p.SourceSeriesTag = strings.TrimSpace(p.SourceSeriesTag)
	p.Alignment = strings.ToLower(strings.TrimSpace(p.Alignment))
	dataType := strings.ToLower(strings.TrimSpace(fallbackDataType))
	if dataType == "kline_resample" {
		p.SourceFrequency = normalizeFrequency(p.SourceFrequency)
		p.TargetFrequency = normalizeFrequency(p.TargetFrequency)
		if p.Alignment == "" {
			p.Alignment = ResampleAlignmentEpochUTC
		}
	}
	if dataType == "kline" {
		p.Frequency = normalizeFrequency(p.Frequency)
	}

	// The source is the target Dataset's subject range. It is no longer a
	// separate symbol Dataset or a persisted subject-set relation.
	p.Source = CollectSource{Kind: "dataset"}
	if dataType == "kline_resample" {
		p.Source.DatasetID = p.SourceDatasetID
	}
	intervals := []string(nil)
	if dataType == "kline_resample" && p.TargetFrequency != "" {
		intervals = []string{p.TargetFrequency}
	} else if p.Frequency != "" {
		intervals = []string{p.Frequency}
	}
	p.Collector = CollectorSpec{
		Exchange:  p.Provider,
		Market:    p.MarketType,
		DataType:  dataType,
		Intervals: intervals,
	}
	p.Target = CollectTarget{DatasetID: p.TargetDatasetID}
	if dataType == "kline_resample" {
		p.Schedule = CollectSchedule{Interval: "1m"}
	} else {
		p.Schedule = CollectSchedule{Interval: p.Frequency}
	}
}

func validateCollectParamsShape(raw string, fallbackDataType string) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil // the strict struct decoder reports the useful JSON error first
	}
	resample := strings.EqualFold(strings.TrimSpace(fallbackDataType), "kline_resample")
	standardOnly := []string{"frequency"}
	resampleOnly := []string{"source_dataset_id", "source_frequency", "source_series_tag", "target_frequency", "alignment", "settle_delay_ms"}
	for _, key := range standardOnly {
		if _, exists := values[key]; exists && resample {
			return fmt.Errorf("parse collect params: field %q is not valid for kline_resample", key)
		}
	}
	for _, key := range resampleOnly {
		if _, exists := values[key]; exists && !resample {
			return fmt.Errorf("parse collect params: field %q is only valid for kline_resample", key)
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ValidateTaskDefinition validates task-level parameters that are independent
// from a concrete Provider route. Direct collection tasks derive Provider and
// market from each selected Tag, so those fields are intentionally not required
// here.
func (p *CollectParams) ValidateTaskDefinition() error {
	if p == nil {
		return fmt.Errorf("collect params are required")
	}
	if p.Collector.DataType == "" {
		return fmt.Errorf("collector data_type is required")
	}
	if p.Target.DatasetID == "" {
		return fmt.Errorf("target.dataset_id is required")
	}
	if _, err := ParseScheduleInterval(p.Schedule.Interval); err != nil {
		return fmt.Errorf("schedule.interval: %w", err)
	}
	switch p.Collector.DataType {
	case "kline":
		if len(p.Collector.Intervals) == 0 {
			return fmt.Errorf("collector.intervals is required for kline")
		}
		for _, value := range p.Collector.Intervals {
			if value == "" {
				return fmt.Errorf("collector.intervals must not contain empty values")
			}
		}
	case "kline_resample":
		return p.ValidateKlineResample()
	default:
		return fmt.Errorf("unsupported collector data_type: %s", p.Collector.DataType)
	}
	return nil
}

// Validate checks the single supported rule JSON contract.
func (p *CollectParams) Validate() error {
	if p == nil {
		return fmt.Errorf("collect params are required")
	}
	if p.Collector.Exchange == "" || p.Collector.Market == "" {
		return fmt.Errorf("collector exchange and market are required")
	}
	return p.ValidateTaskDefinition()
}

// SplitSubjectTags extracts the transient task input. Tags are persisted on
// the result Dataset rather than duplicated in the task's collect_params.
func SplitSubjectTags(raw string) (string, []string, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return "", nil, fmt.Errorf("parse collect params: %w", err)
	}
	var tags []string
	if value, ok := values["subject_tags"]; ok {
		if err := json.Unmarshal(value, &tags); err != nil {
			return "", nil, fmt.Errorf("parse subject_tags: %w", err)
		}
		delete(values, "subject_tags")
	}
	clean, err := json.Marshal(values)
	if err != nil {
		return "", nil, err
	}
	return string(clean), normalizeTags(tags), nil
}

func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	sort.Strings(result)
	return result
}

func normalizeOutputFields(fields []string) []string {
	for i := range fields {
		fields[i] = strings.ToLower(strings.TrimSpace(fields[i]))
	}
	return normalizeTags(fields)
}

// DefaultAllowedLatenessSeconds 返回空间的允许迟到时间默认值：crypto 15 秒，stockcn 90 秒，其余 60 秒。
func DefaultAllowedLatenessSeconds(spaceID string) int {
	switch strings.ToLower(strings.TrimSpace(spaceID)) {
	case "crypto":
		return 15
	case "stockcn":
		return 90
	default:
		return 60
	}
}
