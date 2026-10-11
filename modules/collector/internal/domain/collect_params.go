// Package domain contains Collector control-plane domain types.
package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
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
	HistoryPolicy   *HistoryPolicy  `json:"history_policy,omitempty"`
	// FetchMode 决定实时采集的归属：invoke（默认，由 Collector 派发）或 timer（由 SCF 定时器按环境里的采集计划执行）。
	FetchMode string `json:"fetch_mode,omitempty"`
	// SymbolsPerFunction 是定时模式下每个函数负责的标的数；0 表示使用默认值。
	SymbolsPerFunction int `json:"symbols_per_function,omitempty"`
	// AllowedLatenessSeconds 是结果数据集的允许迟到时间；0 表示使用空间默认值（crypto 15 秒，stockcn 90 秒）。
	AllowedLatenessSeconds int `json:"allowed_lateness_seconds,omitempty"`
	// InvokeUntilPeriod 与 InvokeFromPeriod 用于新旧链路按目标周期（K 线开始时间，RFC3339）交接，
	// 迁移期间使用，迁移完成后清除：旧链路只处理目标周期 < until 且 ≥ from 的周期。
	InvokeUntilPeriod string `json:"invoke_until_period,omitempty"`
	InvokeFromPeriod  string `json:"invoke_from_period,omitempty"`
}

const (
	FetchModeInvoke = "invoke"
	FetchModeTimer  = "timer"
)

// invokeBound 解析一个交接边界；空串返回零值。
func invokeBound(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

// InvokeUntil 返回旧链路的上界（不含）；零值表示没有上界。
func (p *CollectParams) InvokeUntil() time.Time { return invokeBound(p.InvokeUntilPeriod) }

// InvokeFrom 返回旧链路的下界（含）；零值表示没有下界。
func (p *CollectParams) InvokeFrom() time.Time { return invokeBound(p.InvokeFromPeriod) }

// TimerManaged 表示该任务由新对账管理：定时模式，或旧模式但正在向定时模式交接（设置了上界）。
func (p *CollectParams) TimerManaged() bool {
	return p.FetchMode == FetchModeTimer || !p.InvokeUntil().IsZero()
}

// InvokeOwnsPeriod 判断旧的 Invoke 链路是否负责该目标周期。
func (p *CollectParams) InvokeOwnsPeriod(period time.Time) bool {
	period = period.UTC()
	if p.FetchMode == FetchModeTimer {
		// 定时模式下，只有回退窗口（设置了下界）内的周期交给旧链路。
		from := p.InvokeFrom()
		return !from.IsZero() && !period.Before(from)
	}
	if until := p.InvokeUntil(); !until.IsZero() && !period.Before(until) {
		return false
	}
	if from := p.InvokeFrom(); !from.IsZero() && period.Before(from) {
		return false
	}
	return true
}

// InvokeParticipates 表示旧链路在某些周期上仍可能负责该任务（用于任务过滤）。
func (p *CollectParams) InvokeParticipates() bool {
	if p.FetchMode == FetchModeTimer {
		return !p.InvokeFrom().IsZero()
	}
	return true
}

type HistoryPolicy struct {
	Mode              string  `json:"mode"`
	Lookback          int     `json:"lookback,omitempty"`
	Since             string  `json:"since,omitempty"`
	BatchBarLimit     int     `json:"batch_bar_limit,omitempty"`
	MaxConcurrency    int     `json:"max_concurrency,omitempty"`
	GapRepairLookback string  `json:"gap_repair_lookback,omitempty"`
	RateBudgetRatio   float64 `json:"rate_budget_ratio,omitempty"`
}

const (
	HistoryModeLiveOnly = "live_only"
	HistoryModeLookback = "lookback"
	HistoryModeSince    = "since"

	DefaultHistoryBatchBarLimit     = 1000
	DefaultHistoryMaxConcurrency    = 1
	DefaultHistoryGapRepairLookback = "0m"
	DefaultHistoryRateBudgetRatio   = 1.0
)

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
	if err := params.validateFetchMode(); err != nil {
		return nil, err
	}
	return &params, nil
}

func (p *CollectParams) validateFetchMode() error {
	switch p.FetchMode {
	case "", FetchModeInvoke, FetchModeTimer:
	default:
		return fmt.Errorf("parse collect params: fetch_mode must be invoke or timer")
	}
	if p.SymbolsPerFunction < 0 || p.SymbolsPerFunction > 200 {
		return fmt.Errorf("parse collect params: symbols_per_function must be between 0 and 200")
	}
	if p.AllowedLatenessSeconds < 0 || p.AllowedLatenessSeconds > 3600 {
		return fmt.Errorf("parse collect params: allowed_lateness_seconds must be between 0 and 3600")
	}
	for name, raw := range map[string]string{"invoke_until_period": p.InvokeUntilPeriod, "invoke_from_period": p.InvokeFromPeriod} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err != nil {
			return fmt.Errorf("parse collect params: %s must be RFC3339: %w", name, err)
		}
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
	p.FetchMode = strings.ToLower(strings.TrimSpace(p.FetchMode))
	p.InvokeUntilPeriod = strings.TrimSpace(p.InvokeUntilPeriod)
	p.InvokeFromPeriod = strings.TrimSpace(p.InvokeFromPeriod)
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
		p.HistoryPolicy = normalizeHistoryPolicy(p.HistoryPolicy)
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
	standardOnly := []string{"frequency", "history_policy"}
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
	if _, exists := values["history_policy"]; exists && !strings.EqualFold(strings.TrimSpace(fallbackDataType), "kline") {
		return fmt.Errorf("parse collect params: field %q is only valid for kline", "history_policy")
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
		if err := p.ValidateHistoryPolicy(); err != nil {
			return err
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

func normalizeHistoryPolicy(policy *HistoryPolicy) *HistoryPolicy {
	if policy == nil {
		policy = &HistoryPolicy{}
	}
	policy.Mode = strings.ToLower(strings.TrimSpace(policy.Mode))
	if policy.Mode == "" {
		policy.Mode = HistoryModeLiveOnly
	}
	policy.Since = canonicalRFC3339(policy.Since)
	if policy.BatchBarLimit == 0 {
		policy.BatchBarLimit = DefaultHistoryBatchBarLimit
	}
	if policy.MaxConcurrency == 0 {
		policy.MaxConcurrency = DefaultHistoryMaxConcurrency
	}
	if strings.TrimSpace(policy.GapRepairLookback) == "" {
		policy.GapRepairLookback = DefaultHistoryGapRepairLookback
	}
	policy.GapRepairLookback = canonicalNonNegativeDuration(policy.GapRepairLookback)
	if policy.RateBudgetRatio == 0 {
		policy.RateBudgetRatio = DefaultHistoryRateBudgetRatio
	}
	return policy
}

func (p *CollectParams) ValidateHistoryPolicy() error {
	if p == nil {
		return fmt.Errorf("history_policy is required")
	}
	p.HistoryPolicy = normalizeHistoryPolicy(p.HistoryPolicy)
	policy := p.HistoryPolicy
	switch policy.Mode {
	case HistoryModeLiveOnly:
	case HistoryModeLookback:
		if policy.Lookback <= 0 {
			return fmt.Errorf("history_policy.lookback must be greater than 0")
		}
	case HistoryModeSince:
		if strings.TrimSpace(policy.Since) == "" {
			return fmt.Errorf("history_policy.since is required")
		}
		if _, err := time.Parse(time.RFC3339Nano, policy.Since); err != nil {
			return fmt.Errorf("history_policy.since must be RFC3339: %w", err)
		}
	default:
		return fmt.Errorf("history_policy.mode %q is invalid", policy.Mode)
	}
	if policy.BatchBarLimit < 1 || policy.BatchBarLimit > 1000 {
		return fmt.Errorf("history_policy.batch_bar_limit must be between 1 and 1000")
	}
	if policy.MaxConcurrency < 1 || policy.MaxConcurrency > 64 {
		return fmt.Errorf("history_policy.max_concurrency must be between 1 and 64")
	}
	if _, err := parseNonNegativeDuration(policy.GapRepairLookback); err != nil {
		return fmt.Errorf("history_policy.gap_repair_lookback: %w", err)
	}
	if policy.RateBudgetRatio <= 0 || policy.RateBudgetRatio > 1 || math.IsNaN(policy.RateBudgetRatio) || math.IsInf(policy.RateBudgetRatio, 0) {
		return fmt.Errorf("history_policy.rate_budget_ratio must be within (0,1]")
	}
	return nil
}

func canonicalRFC3339(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func canonicalNonNegativeDuration(raw string) string {
	value, err := parseNonNegativeDuration(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return formatDurationCanonical(value)
}

func parseNonNegativeDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("duration is required")
	}
	if raw == "0" || raw == "0m" || raw == "0h" || raw == "0d" || raw == "0w" {
		return 0, nil
	}
	return ParseScheduleInterval(raw)
}

func formatDurationCanonical(value time.Duration) string {
	if value == 0 {
		return "0m"
	}
	if value%(7*24*time.Hour) == 0 {
		return fmt.Sprintf("%dw", int(value/(7*24*time.Hour)))
	}
	if value%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int(value/(24*time.Hour)))
	}
	if value%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(value/time.Hour))
	}
	return fmt.Sprintf("%dm", int(value/time.Minute))
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
