// Package domain contains Collector control-plane domain types.
package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CollectParams describes how a rule generates concrete task instances.
type CollectParams struct {
	Source    CollectSource   `json:"source"`
	Collector CollectorSpec   `json:"collector"`
	Target    CollectTarget   `json:"target"`
	Schedule  CollectSchedule `json:"schedule"`
}

const DefaultCollectorCodePackageID = "moox-collector_dev"

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
}

// CollectTarget describes where collected rows should be written.
type CollectTarget struct {
	DatasetID     string `json:"dataset_id"`
	JobType       string `json:"job_type"`
	CodePackageID string `json:"code_package_id"`
}

// CollectSchedule describes task frequency dimensions.
type CollectSchedule struct {
	Interval  string   `json:"interval"`
	Timezone  string   `json:"timezone"`
	Intervals []string `json:"intervals"`
}

// ParseCollectParams parses rule JSON and normalizes the standard shape.
func ParseCollectParams(raw string, fallbackExchange string, fallbackDataType string) (*CollectParams, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "{}"
	}
	var params CollectParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("parse collect params: %w", err)
	}
	params.Normalize(fallbackExchange, fallbackDataType)
	return &params, nil
}

// Normalize fills defaults for the standard collect params shape.
func (p *CollectParams) Normalize(fallbackExchange string, fallbackDataType string) {
	dataType := p.Collector.DataType
	if dataType == "" {
		dataType = fallbackDataType
	}
	if p.Source.Kind == "" {
		if strings.EqualFold(dataType, "symbol") {
			p.Source.Kind = "none"
		} else {
			p.Source.Kind = "dataset_subjects"
		}
	}
	if p.Collector.Exchange == "" {
		p.Collector.Exchange = fallbackExchange
	}
	if p.Collector.Market == "" {
		p.Collector.Market = "spot"
	}
	if p.Collector.DataType == "" {
		p.Collector.DataType = fallbackDataType
	}
	p.Source.Kind = strings.ToLower(strings.TrimSpace(p.Source.Kind))
	p.Collector.Exchange = strings.ToLower(strings.TrimSpace(p.Collector.Exchange))
	p.Collector.Market = strings.ToLower(strings.TrimSpace(p.Collector.Market))
	p.Collector.DataType = strings.ToLower(strings.TrimSpace(p.Collector.DataType))
	if len(p.Collector.Intervals) == 0 && !strings.EqualFold(p.Collector.DataType, "symbol") {
		p.Collector.Intervals = append([]string(nil), p.Schedule.Intervals...)
	}
	if len(p.Collector.Intervals) == 0 && !strings.EqualFold(p.Collector.DataType, "symbol") {
		p.Collector.Intervals = []string{"1m"}
	}
	if p.Source.DatasetID == "" {
		p.Source.DatasetID = inferDatasetID(p.Collector.Exchange, p.Collector.Market, p.Collector.DataType)
	}
	if p.Target.DatasetID == "" {
		p.Target.DatasetID = p.Source.DatasetID
	}
	if p.Target.JobType == "" {
		p.Target.JobType = "collect." + p.Collector.DataType
	}
	if p.Target.CodePackageID == "" {
		p.Target.CodePackageID = DefaultCollectorCodePackageID
	}
	if p.Schedule.Interval == "" {
		p.Schedule.Interval = "30m"
	}
	if p.Schedule.Timezone == "" {
		p.Schedule.Timezone = "Asia/Shanghai"
	}
}

func inferDatasetID(exchange string, market string, dataType string) string {
	if exchange == "" {
		exchange = "binance"
	}
	if market == "" {
		market = "spot"
	}
	if dataType == "" {
		dataType = "kline"
	}
	return strings.ToLower(exchange + "_" + market + "_" + dataType)
}
