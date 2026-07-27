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
	Source    CollectSource   `json:"source"`
	Collector CollectorSpec   `json:"collector"`
	Target    CollectTarget   `json:"target"`
	Schedule  CollectSchedule `json:"schedule"`
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
func ParseCollectParams(raw string, fallbackExchange string, fallbackDataType string) (*CollectParams, error) {
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
	params.Normalize(fallbackExchange, fallbackDataType)
	return &params, nil
}

// Normalize canonicalizes the standard collect params shape.
func (p *CollectParams) Normalize(fallbackExchange string, fallbackDataType string) {
	if p.Collector.Exchange == "" {
		p.Collector.Exchange = fallbackExchange
	}
	if p.Collector.DataType == "" {
		p.Collector.DataType = fallbackDataType
	}
	p.Source.Kind = strings.ToLower(strings.TrimSpace(p.Source.Kind))
	p.Source.DatasetID = strings.TrimSpace(p.Source.DatasetID)
	p.Collector.Exchange = strings.ToLower(strings.TrimSpace(p.Collector.Exchange))
	p.Collector.Market = strings.ToLower(strings.TrimSpace(p.Collector.Market))
	p.Collector.DataType = strings.ToLower(strings.TrimSpace(p.Collector.DataType))
	for i := range p.Collector.Intervals {
		p.Collector.Intervals[i] = strings.TrimSpace(p.Collector.Intervals[i])
	}
	p.Target.DatasetID = strings.TrimSpace(p.Target.DatasetID)
	p.Schedule.Interval = strings.TrimSpace(p.Schedule.Interval)
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
	default:
		return fmt.Errorf("unsupported collector data_type: %s", p.Collector.DataType)
	}
	return nil
}
