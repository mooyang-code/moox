package main

import (
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/bench"
)

func TestMarkdownReportUsesChineseLabels(t *testing.T) {
	report := benchmarkReport{
		StartedAt:       "2026-06-20T00:00:00Z",
		FinishedAt:      "2026-06-20T00:01:00Z",
		DurationSeconds: 60,
		ZipPath:         "/tmp/kline.zip",
		DataRoot:        "/tmp/data",
		Warnings:        []string{"zip 文件不完整，已使用可恢复 CSV"},
		Datasets: []datasetInfo{{
			Market:    "spot",
			DatasetID: "bench_binance_spot_kline_1h",
			ViewID:    "bench_spot_kline_view",
			Rows:      100,
		}},
		MetadataReady: metadataReadyReport{DurationSeconds: 10, Attempts: 3, RefreshIntervalHint: "snapshotcache 默认 10s", TimeoutSeconds: 35},
		Write:         writeReport{Rows: 100, Batches: 1, DurationSeconds: 2, RowsPerSecond: 50, BatchLatency: benchLatency(10, 20, 30, 40)},
		ObjectWrite:   writeReport{Rows: 80, Batches: 1, DurationSeconds: 1, RowsPerSecond: 80, BatchLatency: benchLatency(11, 21, 31, 41)},
		PrimaryRead: operationReport{
			Requests: 10, Concurrency: 2, RowsReturned: 1000, RequestsPerSec: 20, RowsPerSecond: 2000, Latency: benchLatency(12, 22, 32, 42),
		},
		PrimaryKlinePoint: operationReport{
			Requests: 10, Concurrency: 2, RowsReturned: 10, RequestsPerSec: 180, RowsPerSecond: 180, Latency: benchLatency(14, 24, 34, 44),
		},
		KlinePointTarget: querySample{
			Market: "spot", DatasetID: "bench_binance_spot_kline_1h", SubjectID: "BTC-USDT", DataTime: "2025-03-03T00:00:00Z", RowID: "2025-03-03T00:00:00Z",
		},
		DuckDBView: duckDBReport{
			Verified:     true,
			ExpectedRows: 100,
			Materialized: map[string]uint64{"bench_binance_spot_kline_1h": 100},
			QueryBenchmark: operationReport{
				Requests: 10, Concurrency: 2, RowsReturned: 1000, RequestsPerSec: 18, RowsPerSecond: 1800, Latency: benchLatency(15, 25, 35, 45),
			},
		},
		StorageWorkDir: "/tmp/work",
		StorageLogPath: "/tmp/work/server.log",
	}

	got := markdownReport(report)
	for _, want := range []string{
		"# MooX Storage 压测报告",
		"## 注意事项",
		"## 数据集",
		"## 元数据缓存预热",
		"缓存刷新周期参考",
		"等待耗时",
		"探测轮次",
		"## 时序 K 线写入性能",
		"## 非时序对象写入性能",
		"## Primary 主存读取性能",
		"## Primary 单根 K 线查询性能",
		"## DuckDB 视图读取性能",
		"## 运行环境",
		"吞吐",
		"行吞吐",
		"目标标的",
		"平均延迟",
		"P50 延迟",
		"P95 延迟",
		"P99 延迟",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown report should contain %q:\n%s", want, got)
		}
	}
	for _, unexpected := range []string{
		"# MooX Storage Benchmark Report",
		"## Write",
		"## Primary ReadRows",
		"## Primary Point ReadRows",
		"## DuckDB QueryView",
	} {
		if strings.Contains(got, unexpected) {
			t.Fatalf("markdown report should not contain %q:\n%s", unexpected, got)
		}
	}
}

func benchLatency(avg float64, p50 float64, p95 float64, p99 float64) bench.LatencySummary {
	return bench.LatencySummary{AvgMS: avg, P50MS: p50, P95MS: p95, P99MS: p99}
}

func TestLocalizedExtractOutputUsesChineseExplanation(t *testing.T) {
	got := localizedExtractOutput("bsdtar: Truncated input file (needed 188 bytes, only 134 available)\nbsdtar: Error exit delayed from previous errors.")
	for _, want := range []string{"解压工具输出", "输入文件疑似截断", "需要 188 字节，但文件仅有 134 字节", "因前述错误退出"} {
		if !strings.Contains(got, want) {
			t.Fatalf("localized output should contain %q: %s", want, got)
		}
	}
	if strings.Contains(got, "Truncated input file") || strings.Contains(got, "Error exit delayed") || strings.Contains(got, "needed 188 bytes") {
		t.Fatalf("localized output should not keep raw English summary: %s", got)
	}
}

func TestSelectKlinePointTargetHonorsFilters(t *testing.T) {
	samples := []querySample{
		{Market: "spot", DatasetID: "bench_binance_spot_kline_1h", SubjectID: "BNB-USDT", DataTime: "2025-03-03T00:00:00Z", RowID: "2025-03-03T00:00:00Z"},
		{Market: "swap", DatasetID: "bench_binance_swap_kline_1h", SubjectID: "BTC-USDT", DataTime: "2025-03-03T01:00:00Z", RowID: "2025-03-03T01:00:00Z"},
	}

	got, err := selectKlinePointTarget(samples, options{targetMarket: "swap", targetSubject: "BTC-USDT", targetTime: "2025-03-03T01:00:00Z"})
	if err != nil {
		t.Fatalf("select target: %v", err)
	}
	if got.Market != "swap" || got.SubjectID != "BTC-USDT" || got.DataTime != "2025-03-03T01:00:00Z" {
		t.Fatalf("target = %+v", got)
	}
}
