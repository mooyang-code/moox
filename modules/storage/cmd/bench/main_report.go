package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/bench"
)

type metadataReadyReport struct {
	DurationSeconds     float64 `json:"duration_seconds"`
	Attempts            int     `json:"attempts"`
	RefreshIntervalHint string  `json:"refresh_interval_hint"`
	TimeoutSeconds      float64 `json:"timeout_seconds"`
}

type writeReport struct {
	Rows             int                  `json:"rows"`
	Batches          int                  `json:"batches"`
	DurationSeconds  float64              `json:"duration_seconds"`
	RowsPerSecond    float64              `json:"rows_per_second"`
	SteadyRows       int                  `json:"steady_rows,omitempty"`
	SteadySeconds    float64              `json:"steady_seconds,omitempty"`
	SteadyRowsPerSec float64              `json:"steady_rows_per_second,omitempty"`
	SlowestBatchMS   float64              `json:"slowest_batch_ms,omitempty"`
	BatchLatency     bench.LatencySummary `json:"batch_latency"`
}

type operationReport struct {
	Requests        int                  `json:"requests"`
	Concurrency     int                  `json:"concurrency"`
	PageSize        uint32               `json:"page_size"`
	RowsReturned    int                  `json:"rows_returned"`
	DurationSeconds float64              `json:"duration_seconds"`
	RequestsPerSec  float64              `json:"requests_per_second"`
	RowsPerSecond   float64              `json:"rows_per_second"`
	Latency         bench.LatencySummary `json:"latency"`
}

type duckDBReport struct {
	Verified       bool              `json:"verified"`
	ExpectedRows   int               `json:"expected_rows"`
	Materialized   map[string]uint64 `json:"materialized_rows"`
	QueryBenchmark operationReport   `json:"query_benchmark"`
}

func finishWriteReport(report *writeReport, recorder bench.LatencyRecorder, batchRows []int, batchDurations []time.Duration) {
	report.BatchLatency = recorder.Summary()
	var total time.Duration
	var slowest time.Duration
	slowestIndex := -1
	for i, elapsed := range batchDurations {
		total += elapsed
		if elapsed > slowest {
			slowest = elapsed
			slowestIndex = i
		}
	}
	report.DurationSeconds = total.Seconds()
	if report.DurationSeconds > 0 {
		report.RowsPerSecond = float64(report.Rows) / report.DurationSeconds
	}
	report.SlowestBatchMS = float64(slowest) / float64(time.Millisecond)
	if len(batchDurations) <= 1 || slowestIndex < 0 {
		return
	}
	steadyRows := report.Rows - batchRows[slowestIndex]
	steadyDuration := total - slowest
	if steadyRows <= 0 || steadyDuration <= 0 {
		return
	}
	report.SteadyRows = steadyRows
	report.SteadySeconds = steadyDuration.Seconds()
	report.SteadyRowsPerSec = float64(steadyRows) / report.SteadySeconds
}

func writeReports(dir string, report *benchmarkReport) error {
	stamp := time.Now().Format("20060102-150405")
	jsonPath := filepath.Join(dir, "storage-bench-"+stamp+".json")
	mdPath := filepath.Join(dir, "storage-bench-"+stamp+".md")
	report.ReportJSONPath = jsonPath
	report.ReportMarkdown = mdPath
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		return err
	}
	return os.WriteFile(mdPath, []byte(markdownReport(*report)), 0o644)
}

func markdownReport(report benchmarkReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# MooX Storage 压测报告\n\n")
	fmt.Fprintf(&b, "- 开始时间: `%s`\n- 结束时间: `%s`\n- 总耗时: `%.2fs`\n- 数据目录: `%s`\n- 数据压缩包: `%s`\n\n", report.StartedAt, report.FinishedAt, report.DurationSeconds, report.DataRoot, report.ZipPath)
	if len(report.Warnings) > 0 {
		fmt.Fprintf(&b, "## 注意事项\n\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
		fmt.Fprintf(&b, "\n")
	}
	fmt.Fprintf(&b, "## 数据集\n\n| 市场 | Dataset | View | 行数 |\n| --- | --- | --- | ---: |\n")
	for _, dataset := range report.Datasets {
		fmt.Fprintf(&b, "| %s | `%s` | `%s` | %d |\n", dataset.Market, dataset.DatasetID, dataset.ViewID, dataset.Rows)
	}
	fmt.Fprintf(&b, "\n")
	appendMetadataReadySection(&b, report.MetadataReady)
	appendWriteSection(&b, "## 时序 K 线写入性能", "场景: `WriteTimeSeriesRows`，K 线数据按 `subject_id + freq + data_time` 写入", report.Write)
	appendWriteSection(&b, "## 非时序记录写入性能", fmt.Sprintf("场景: `WriteRecordRows`，合成记录数据写入 `%s`，按 `record_id + version` 定位", recordDatasetID), report.RecordWrite)
	fmt.Fprintf(&b, "## Primary 主存读取性能\n\n- 场景: `ReadTimeSeriesRows` 首页读取，默认每次返回 `%d` 行\n- 请求数: `%d`\n- 并发数: `%d`\n- 返回行数: `%d`\n- 平均请求吞吐: `%.2f req/s`\n- 平均行吞吐: `%.2f rows/s`\n", report.PrimaryRead.PageSize, report.PrimaryRead.Requests, report.PrimaryRead.Concurrency, report.PrimaryRead.RowsReturned, report.PrimaryRead.RequestsPerSec, report.PrimaryRead.RowsPerSecond)
	appendLatencyLines(&b, "读取", report.PrimaryRead.Latency)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "## Primary 单根 K 线查询性能\n\n- 场景: `ReadTimeSeriesRows` + 固定标的 + 固定 `data_time` 精确 TimeRange，每次返回 1 根 K 线\n- 目标市场: `%s`\n- 目标标的: `%s`\n- 目标时间: `%s`\n- 请求数: `%d`\n- 并发数: `%d`\n- 返回行数: `%d`\n- 平均请求吞吐: `%.2f req/s`\n- 平均行吞吐: `%.2f rows/s`\n", report.KlinePointTarget.Market, report.KlinePointTarget.SubjectID, report.KlinePointTarget.DataTime, report.PrimaryKlinePoint.Requests, report.PrimaryKlinePoint.Concurrency, report.PrimaryKlinePoint.RowsReturned, report.PrimaryKlinePoint.RequestsPerSec, report.PrimaryKlinePoint.RowsPerSecond)
	appendLatencyLines(&b, "查询", report.PrimaryKlinePoint.Latency)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "## DuckDB 视图读取性能\n\n- 异步物化验证: `%t`\n- 预期物化行数: `%d`\n\n| Dataset | DuckDB 物化行数 |\n| --- | ---: |\n", report.DuckDBView.Verified, report.DuckDBView.ExpectedRows)
	keys := make([]string, 0, len(report.DuckDBView.Materialized))
	for key := range report.DuckDBView.Materialized {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "| `%s` | %d |\n", key, report.DuckDBView.Materialized[key])
	}
	fmt.Fprintf(&b, "\n- QueryTimeSeriesRows 请求数: `%d`\n- 并发数: `%d`\n- 返回行数: `%d`\n- 平均查询吞吐: `%.2f req/s`\n- 平均行吞吐: `%.2f rows/s`\n", report.DuckDBView.QueryBenchmark.Requests, report.DuckDBView.QueryBenchmark.Concurrency, report.DuckDBView.QueryBenchmark.RowsReturned, report.DuckDBView.QueryBenchmark.RequestsPerSec, report.DuckDBView.QueryBenchmark.RowsPerSecond)
	appendLatencyLines(&b, "查询", report.DuckDBView.QueryBenchmark.Latency)
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "## 运行环境\n\n- Storage 工作目录: `%s`\n- Storage 日志: `%s`\n", report.StorageWorkDir, report.StorageLogPath)
	return b.String()
}

func appendMetadataReadySection(b *strings.Builder, report metadataReadyReport) {
	fmt.Fprintf(b, "## 元数据缓存预热\n\n")
	fmt.Fprintf(b, "- 缓存刷新周期参考: `%s`\n", report.RefreshIntervalHint)
	fmt.Fprintf(b, "- 等待超时: `%.2fs`\n", report.TimeoutSeconds)
	fmt.Fprintf(b, "- 等待耗时: `%.2fs`\n", report.DurationSeconds)
	fmt.Fprintf(b, "- 探测轮次: `%d`\n", report.Attempts)
	fmt.Fprintf(b, "- 说明: `正式写入压测在 Access 校验与路由可见后开始，预热探针不计入写入性能`\n\n")
}

func appendWriteSection(b *strings.Builder, title string, scene string, report writeReport) {
	fmt.Fprintf(b, "%s\n\n- %s\n- 写入行数: `%d`\n- 写入批次: `%d`\n- 写入耗时: `%.2fs`\n- 平均写入吞吐: `%.2f rows/s`\n- 最慢批次: `%.2f ms`\n",
		title, scene, report.Rows, report.Batches, report.DurationSeconds, report.RowsPerSecond, report.SlowestBatchMS)
	appendLatencyLines(b, "批次", report.BatchLatency)
	if report.SteadyRows > 0 {
		fmt.Fprintf(b, "- 稳态写入行数: `%d`\n- 稳态写入耗时: `%.2fs`\n- 稳态吞吐（排除最慢批次）: `%.2f rows/s`\n", report.SteadyRows, report.SteadySeconds, report.SteadyRowsPerSec)
	}
	fmt.Fprintf(b, "\n")
}

func appendLatencyLines(b *strings.Builder, label string, latency bench.LatencySummary) {
	fmt.Fprintf(b, "- 平均延迟: `%.2f ms`\n", latency.AvgMS)
	fmt.Fprintf(b, "- P50 延迟: `%.2f ms`\n", latency.P50MS)
	fmt.Fprintf(b, "- P95 延迟: `%.2f ms`\n", latency.P95MS)
	fmt.Fprintf(b, "- P99 延迟: `%.2f ms`\n", latency.P99MS)
	if label != "" {
		fmt.Fprintf(b, "- %s样本数: `%d`\n", label, latency.Count)
	}
}
