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
	SymbolSource    string          `json:"symbol_source,omitempty"`
	Symbols         []string        `json:"symbols,omitempty"`
	SymbolDatasetID string          `json:"symbol_dataset_id,omitempty"`
	TargetDatasetID string          `json:"target_dataset_id,omitempty"`
	Frequency       string          `json:"frequency,omitempty"`
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
	params.Normalize(fallbackProvider, fallbackMarketType, fallbackDataType)
	return &params, nil
}

// Normalize canonicalizes the standard collect params shape.
func (p *CollectParams) Normalize(fallbackProvider string, fallbackMarketType string, fallbackDataType string) {
	p.Provider = strings.ToLower(firstNonEmpty(p.Provider, fallbackProvider))
	p.MarketType = strings.ToLower(firstNonEmpty(p.MarketType, fallbackMarketType))
	p.SymbolSource = strings.ToLower(strings.TrimSpace(p.SymbolSource))
	p.SymbolDatasetID = strings.TrimSpace(p.SymbolDatasetID)
	p.TargetDatasetID = strings.TrimSpace(p.TargetDatasetID)
	p.Frequency = strings.TrimSpace(p.Frequency)
	if p.Frequency == "" && strings.EqualFold(strings.TrimSpace(fallbackDataType), "symbol") {
		// Symbol snapshots are deliberately small manual inventories. They use a
		// fixed hourly cadence unless a valid explicit frequency is supplied.
		p.Frequency = "1h"
	}

	sourceKind := ""
	switch p.SymbolSource {
	case "dataset":
		sourceKind = "dataset_subjects"
	case "manual", "exchange":
		sourceKind = "none"
	}
	p.Source = CollectSource{Kind: sourceKind, DatasetID: p.SymbolDatasetID}
	intervals := []string(nil)
	if p.Frequency != "" {
		intervals = []string{p.Frequency}
	}
	p.Collector = CollectorSpec{
		Exchange:  p.Provider,
		Market:    p.MarketType,
		DataType:  strings.ToLower(strings.TrimSpace(fallbackDataType)),
		Intervals: intervals,
	}
	p.Target = CollectTarget{DatasetID: p.TargetDatasetID}
	p.Schedule = CollectSchedule{Interval: p.Frequency}
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
		switch p.SymbolSource {
		case "manual":
			if len(p.Symbols) == 0 {
				return fmt.Errorf("symbols is required for manual symbol source")
			}
		case "exchange":
			if len(p.Symbols) != 0 {
				return fmt.Errorf("symbols must be empty for exchange symbol source")
			}
		default:
			return fmt.Errorf("symbol_source must be manual or exchange for symbol task")
		}
	default:
		return fmt.Errorf("unsupported collector data_type: %s", p.Collector.DataType)
	}
	return nil
}
