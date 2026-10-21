package marketfetch

import (
	"sort"
	"strings"
)

// TaskGroup is one independent timer workload. A function must never mix
// datasets, frequencies, or market types because those values are part of the
// function's static environment.
type TaskGroup struct {
	Provider        string
	MarketType      string
	MarketID        string
	InstrumentType  string
	SourceID        string
	SeriesTag       string
	DatasetID       string
	Frequency       string
	OutputFields    []string
	Subjects        []string
	ExternalSymbols map[string]string
	// Pairs 是交易对与交易所 symbol 不同的标的（币安永续合约的 pair），只下发给 WS 推送。
	Pairs map[string]string
	// 以下为定时采集计划使用：每函数标的数（0 为默认）、允许迟到时间（0 为空间默认）。
	SymbolsPerFunction     int
	AllowedLatenessSeconds int
	// Ingest、Executor 是任务参数显式指定的采集方式与执行端，空表示按库里的数据集设置。
	Ingest   string
	Executor string
}

func normalizeSubjects(subjects []string) []string {
	seen := make(map[string]struct{}, len(subjects))
	result := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		subject = strings.ToUpper(strings.TrimSpace(subject))
		if subject == "" {
			continue
		}
		if _, ok := seen[subject]; ok {
			continue
		}
		seen[subject] = struct{}{}
		result = append(result, subject)
	}
	sort.Strings(result)
	return result
}

func groupKey(group TaskGroup) string {
	return strings.Join([]string{group.Provider, group.MarketType, group.MarketID, group.InstrumentType, group.SourceID, group.SeriesTag, group.DatasetID, group.Frequency, strings.Join(normalizeOutputFields(group.OutputFields), ",")}, "\x00")
}

func normalizeOutputFields(fields []string) []string {
	seen := make(map[string]struct{}, len(fields))
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.ToLower(strings.TrimSpace(field))
		if field == "" {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		result = append(result, field)
	}
	sort.Strings(result)
	return result
}
