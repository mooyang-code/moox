package marketfetch

import (
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/packages/clsreporter"
)

// 失败明细与批次摘要上报 CLS：函数日志不会自动投递，排查“为什么某个标的这一分钟没采到”需要按标的、频率、
// 错误类型检索。上报是同步一次性的（调用结束时 Flush），失败不影响采集。
const (
	clsLogTypeItemFailure = "market_fetch_item_failure"
	clsLogTypeBatch       = "market_fetch_batch"
	clsErrorLimit         = 600
)

// reportItemFailure 上报一个失败项：包含错误全文、尝试次数与取数耗时，用来判断重试是否有效。
func (p *KlinePipeline) reportItemFailure(req Request, result domain.ItemResult, provider string, attempts int, elapsed time.Duration, windowStart, windowEnd time.Time, cause error) {
	if p == nil || p.Reporter == nil {
		return
	}
	message := ""
	if cause != nil {
		message = truncateString(cause.Error(), clsErrorLimit)
	}
	p.Reporter.Report(clsreporter.Entry{Fields: map[string]string{
		"log_type": clsLogTypeItemFailure, "space_id": firstNonEmptyString(p.SpaceID, req.SpaceID), "function": p.FunctionName,
		"batch_kind": string(req.BatchKind), "request_id": req.RequestID, "dataset_id": req.DatasetID, "frequency": req.Frequency,
		"subject_id": result.SubjectID, "symbol": result.Symbol, "provider": provider,
		"outcome": string(result.Outcome), "error_type": result.ErrorType, "error": message,
		"attempts": strconv.Itoa(attempts), "fetch_ms": strconv.FormatInt(elapsed.Milliseconds(), 10),
		"window_start": formatReportTime(windowStart), "window_end": formatReportTime(windowEnd),
	}})
}

// reportBatch 上报一次取数批次的摘要：条数、失败数、取数耗时与剩余窗口，用来统计整体效率。
func (p *KlinePipeline) reportBatch(req Request, planned, failed int, fetch time.Duration, windowLeft time.Duration, extra ...map[string]string) {
	if p == nil || p.Reporter == nil {
		return
	}
	fields := map[string]string{
		"log_type": clsLogTypeBatch, "space_id": firstNonEmptyString(p.SpaceID, req.SpaceID), "function": p.FunctionName,
		"batch_kind": string(req.BatchKind), "request_id": req.RequestID, "dataset_id": req.DatasetID, "frequency": req.Frequency,
		"planned": strconv.Itoa(planned), "failed": strconv.Itoa(failed),
		"fetch_ms": strconv.FormatInt(fetch.Milliseconds(), 10), "window_left_ms": strconv.FormatInt(windowLeft.Milliseconds(), 10),
	}
	// 取数项的阶段耗时汇总（排队、DNS、建连、TLS、首字节、重试），见 cls_timing.go。
	for _, more := range extra {
		for key, value := range more {
			fields[key] = value
		}
	}
	// 已用权重：行情源按出口 IP 统计的当前分钟总用量（含同一 IP 上其他函数），用来估算离限额还有多远。
	if used := marketdata.TakeUsedWeight(); used > 0 {
		fields["used_weight"] = strconv.Itoa(used)
	}
	p.Reporter.Report(clsreporter.Entry{Fields: fields})
}

func formatReportTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
