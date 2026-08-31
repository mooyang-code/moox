// Package domain contains Collector control-plane domain types.
package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	SymbolSource    string          `json:"symbol_source,omitempty"`
	SymbolDatasetID string          `json:"symbol_dataset_id,omitempty"`
	TargetDatasetID string          `json:"target_dataset_id,omitempty"`
	Frequency       string          `json:"frequency,omitempty"`
	SourceDatasetID string          `json:"source_dataset_id,omitempty"`
	SourceFrequency string          `json:"source_frequency,omitempty"`
	SourceSeriesTag string          `json:"source_series_tag,omitempty"`
	TargetFrequency string          `json:"target_frequency,omitempty"`
	Alignment       string          `json:"alignment,omitempty"`
	SettleDelayMS   *int64          `json:"settle_delay_ms,omitempty"`
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
	return &params, nil
}

// Normalize canonicalizes the standard collect params shape.
func (p *CollectParams) Normalize(fallbackProvider string, fallbackMarketType string, fallbackDataType string) {
	p.Provider = strings.ToLower(firstNonEmpty(p.Provider, fallbackProvider))
	p.MarketType = strings.ToLower(firstNonEmpty(p.MarketType, fallbackMarketType))
	p.MarketID = strings.ToLower(strings.TrimSpace(p.MarketID))
	p.InstrumentType = strings.ToLower(strings.TrimSpace(p.InstrumentType))
	p.SourceID = strings.ToLower(strings.TrimSpace(p.SourceID))
	p.SeriesTag = strings.TrimSpace(p.SeriesTag)
	p.SymbolSource = strings.ToLower(strings.TrimSpace(p.SymbolSource))
	p.SymbolDatasetID = strings.TrimSpace(p.SymbolDatasetID)
	p.TargetDatasetID = strings.TrimSpace(p.TargetDatasetID)
	p.Frequency = strings.TrimSpace(p.Frequency)
	p.SourceDatasetID = strings.TrimSpace(p.SourceDatasetID)
	p.SourceSeriesTag = strings.TrimSpace(p.SourceSeriesTag)
	p.Alignment = strings.ToLower(strings.TrimSpace(p.Alignment))
	dataType := strings.ToLower(strings.TrimSpace(fallbackDataType))
	if dataType == "kline_resample" {
		p.SourceFrequency = normalizeFixedFrequency(p.SourceFrequency)
		p.TargetFrequency = normalizeFixedFrequency(p.TargetFrequency)
		if p.Alignment == "" {
			p.Alignment = ResampleAlignmentEpochUTC
		}
	}
	if p.Frequency == "" && strings.EqualFold(strings.TrimSpace(fallbackDataType), "symbol") {
		// Full Symbol snapshots use an hourly cadence unless a valid explicit
		// frequency is supplied.
		p.Frequency = "1h"
	}

	sourceKind := ""
	switch p.SymbolSource {
	case "dataset":
		sourceKind = "dataset_subjects"
	case "exchange", "manual":
		sourceKind = "none"
	}
	if dataType == "kline_resample" {
		sourceKind = "dataset_subjects"
		p.Source = CollectSource{Kind: sourceKind, DatasetID: p.SourceDatasetID}
	} else {
		p.Source = CollectSource{Kind: sourceKind, DatasetID: p.SymbolDatasetID}
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
	standardOnly := []string{"symbol_source", "symbol_dataset_id", "frequency"}
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

// Validate checks the single supported rule JSON contract.
func (p *CollectParams) Validate() error {
	if p == nil {
		return fmt.Errorf("collect params are required")
	}
	if p.Collector.Exchange == "" || p.Collector.Market == "" || p.Collector.DataType == "" {
		return fmt.Errorf("collector exchange, market and data_type are required")
	}
	if p.Target.DatasetID == "" {
		return fmt.Errorf("target.dataset_id is required")
	}
	if _, err := ParseScheduleInterval(p.Schedule.Interval); err != nil {
		return fmt.Errorf("schedule.interval: %w", err)
	}
	switch p.Collector.DataType {
	case "kline":
		if p.Source.Kind != "dataset_subjects" || p.Source.DatasetID == "" {
			return fmt.Errorf("kline source.dataset_id is required with source.kind=dataset_subjects")
		}
		if len(p.Collector.Intervals) == 0 {
			return fmt.Errorf("collector.intervals is required for kline")
		}
		for _, value := range p.Collector.Intervals {
			if value == "" {
				return fmt.Errorf("collector.intervals must not contain empty values")
			}
		}
	case "symbol":
		if p.Source.Kind != "none" {
			return fmt.Errorf("symbol source.kind must be none")
		}
		if p.SymbolSource != "exchange" {
			return fmt.Errorf("symbol_source must be exchange for symbol task")
		}
	case "kline_resample":
		return p.ValidateKlineResample()
	default:
		return fmt.Errorf("unsupported collector data_type: %s", p.Collector.DataType)
	}
	return nil
}
