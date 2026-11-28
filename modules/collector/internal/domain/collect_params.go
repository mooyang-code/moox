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

// CollectParams 描述一条采集或合成任务的持久化参数。
// 任务与标签、结果数据集的关联保存在独立列与关联表中，不重复写进这里。
type CollectParams struct {
	OutputFields []string `json:"output_fields,omitempty"`
	Frequency    string   `json:"frequency,omitempty"`
	// SourceTaskID 是合成的源采集任务；source_dataset_id、source_frequency、source_series_tag 在创建时由源任务推导并定格保存：
	// 保证目标数据集的血缘不可变（源任务切换主来源后不会混入新 series_tag），且源任务被 --keep-result 删除后合成任务与结果清单仍可独立工作。
	SourceTaskID    string `json:"source_task_id,omitempty"`
	SourceDatasetID string `json:"source_dataset_id,omitempty"`
	SourceFrequency string `json:"source_frequency,omitempty"`
	SourceSeriesTag string `json:"source_series_tag,omitempty"`
	TargetFrequency string `json:"target_frequency,omitempty"`
	// SymbolsPerFunction 是定时模式下每个函数负责的标的数；0 表示使用默认值。
	SymbolsPerFunction int `json:"symbols_per_function,omitempty"`
	// AllowedLatenessSeconds 是结果数据集的允许迟到时间；0 表示使用市场 market.yaml 里的默认值。
	AllowedLatenessSeconds int `json:"allowed_lateness_seconds,omitempty"`
}

// ParseCollectParams 解析并规范化任务参数 JSON；未知字段与不匹配的字段组合直接报错。
func ParseCollectParams(raw string, dataType string) (*CollectParams, error) {
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
	if err := validateCollectParamsShape(raw, dataType); err != nil {
		return nil, err
	}
	params.Normalize(dataType)
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
	return nil
}

// Normalize 规范化参数里的空白、大小写与频率写法。
func (p *CollectParams) Normalize(dataType string) {
	p.OutputFields = normalizeOutputFields(p.OutputFields)
	p.Frequency = strings.TrimSpace(p.Frequency)
	p.SourceTaskID = strings.TrimSpace(p.SourceTaskID)
	p.SourceDatasetID = strings.TrimSpace(p.SourceDatasetID)
	p.SourceSeriesTag = strings.TrimSpace(p.SourceSeriesTag)
	dt := strings.ToLower(strings.TrimSpace(dataType))
	if dt == "kline_resample" {
		p.SourceFrequency = normalizeFrequency(p.SourceFrequency)
		p.TargetFrequency = normalizeFrequency(p.TargetFrequency)
	}
	if dt == DataTypeKline {
		p.Frequency = normalizeFrequency(p.Frequency)
	}
}

func validateCollectParamsShape(raw string, dataType string) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	resample := strings.EqualFold(strings.TrimSpace(dataType), "kline_resample")
	standardOnly := []string{"frequency"}
	resampleOnly := []string{"source_task_id", "source_dataset_id", "source_frequency", "source_series_tag", "target_frequency"}
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

// ValidateKline 校验 K 线采集任务的参数。
func (p *CollectParams) ValidateKline() error {
	if p == nil {
		return fmt.Errorf("collect params are required")
	}
	if p.Frequency == "" {
		return fmt.Errorf("frequency is required for kline")
	}
	if _, err := ParseScheduleInterval(p.Frequency); err != nil {
		return fmt.Errorf("frequency: %w", err)
	}
	return nil
}

func normalizeUniqueSorted(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

// FieldFundingRate 是可选输出字段“资金费率”：只在显式选中时采集与写入，输出字段为空（表示默认字段）时不包含。
const FieldFundingRate = "funding_rate"

// HasOutputField 判断输出字段里是否显式选了 field。
func HasOutputField(fields []string, field string) bool {
	for _, candidate := range fields {
		if strings.EqualFold(strings.TrimSpace(candidate), field) {
			return true
		}
	}
	return false
}

func normalizeOutputFields(fields []string) []string {
	for i := range fields {
		fields[i] = strings.ToLower(strings.TrimSpace(fields[i]))
	}
	return normalizeUniqueSorted(fields)
}
