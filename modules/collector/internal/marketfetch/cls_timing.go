package marketfetch

import (
	"sort"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/packages/clsreporter"
)

// 取数阶段耗时上报：整点前后行情源响应变慢时，批次摘要只能看到“整批取数用了几秒”，看不出慢在排队、DNS、建连、
// TLS 握手还是行情源的首字节，也看不出有没有重试。这里把每个取数项的阶段耗时汇总进批次摘要，并把偏慢或重试过的
// 单项（每批最多 maxSlowItemReports 条）单独上报，量级与失败明细同级，不逐项上报正常请求。
const (
	clsLogTypeItemSlow = "market_fetch_item_slow"
	clsLogTypeCommit   = "market_fetch_commit"

	// slowItemThreshold 是单项取数耗时的上报下限；重试过（尝试次数或 HTTP 请求数大于 1）的项不论快慢都上报。
	slowItemThreshold = time.Second
	// maxSlowItemReports 限制每个批次上报的偏慢单项条数，取最慢的几条。
	maxSlowItemReports = 8
)

// itemTiming 是一个取数项的阶段耗时。
type itemTiming struct {
	// Recorded 表示该项走到了取数这一步（符号缺失等提前返回的项没有耗时）。
	Recorded bool
	Subject  string
	Symbol   string
	Provider string
	Outcome  domain.ItemOutcome
	// Queue 是等待并发名额的时间，Fetch 是取数（含重试与切换端点）的总耗时。
	Queue, Fetch time.Duration
	// StartedAt 是开始取数的时刻，用来换算相对收盘的偏移。
	StartedAt time.Time
	Attempts  int
	HTTP      httpclient.TraceSnapshot
}

// batchTimingFields 把整批取数项的阶段耗时汇总成批次摘要的附加字段。
func batchTimingFields(items []itemTiming, batchStart, fetchEnd, closeAt time.Time) map[string]string {
	fields := map[string]string{}
	if !closeAt.IsZero() {
		fields["since_close_start_ms"] = strconv.FormatInt(batchStart.Sub(closeAt).Milliseconds(), 10)
		fields["since_close_fetch_end_ms"] = strconv.FormatInt(fetchEnd.Sub(closeAt).Milliseconds(), 10)
	}
	var fetch, queue, connect, tlsMs, dns, ttfb []int64
	attempts, retried, requests, newConns, reusedConns := 0, 0, 0, 0, 0
	for _, item := range items {
		if !item.Recorded {
			continue
		}
		fetch = append(fetch, item.Fetch.Milliseconds())
		queue = append(queue, item.Queue.Milliseconds())
		attempts += item.Attempts
		if item.Attempts > 1 || item.HTTP.Requests > 1 {
			retried++
		}
		requests += item.HTTP.Requests
		newConns += item.HTTP.NewConns
		reusedConns += item.HTTP.ReusedConns
		dns = append(dns, item.HTTP.DNSMs)
		connect = append(connect, item.HTTP.ConnectMs)
		tlsMs = append(tlsMs, item.HTTP.TLSMs)
		if item.HTTP.Requests > 0 {
			ttfb = append(ttfb, item.HTTP.TTFBMs)
		}
	}
	if len(fetch) == 0 {
		return fields
	}
	set := func(key string, value int64) { fields[key] = strconv.FormatInt(value, 10) }
	set("item_p50_ms", percentile(fetch, 50))
	set("item_p90_ms", percentile(fetch, 90))
	set("item_max_ms", percentile(fetch, 100))
	set("queue_max_ms", percentile(queue, 100))
	set("dns_max_ms", percentile(dns, 100))
	set("connect_p90_ms", percentile(connect, 90))
	set("connect_max_ms", percentile(connect, 100))
	set("tls_p90_ms", percentile(tlsMs, 90))
	set("tls_max_ms", percentile(tlsMs, 100))
	if len(ttfb) > 0 {
		set("ttfb_p50_ms", percentile(ttfb, 50))
		set("ttfb_p90_ms", percentile(ttfb, 90))
		set("ttfb_max_ms", percentile(ttfb, 100))
	}
	set("attempts_total", int64(attempts))
	set("retried", int64(retried))
	set("http_requests", int64(requests))
	set("conn_new", int64(newConns))
	set("conn_reused", int64(reusedConns))
	return fields
}

// percentile 返回已有样本里的第 p 百分位（最近秩法）；样本为空返回 0。
func percentile(values []int64, p int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := (len(sorted)*p + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// reportSlowItems 上报偏慢或重试过的取数项，最慢的排前面，最多 maxSlowItemReports 条。
func (p *KlinePipeline) reportSlowItems(req Request, items []itemTiming, closeAt time.Time) {
	if p == nil || p.Reporter == nil {
		return
	}
	var picked []itemTiming
	for _, item := range items {
		if item.Recorded && (item.Fetch >= slowItemThreshold || item.Attempts > 1 || item.HTTP.Requests > 1) {
			picked = append(picked, item)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].Fetch > picked[j].Fetch })
	if len(picked) > maxSlowItemReports {
		picked = picked[:maxSlowItemReports]
	}
	for _, item := range picked {
		fields := map[string]string{
			"log_type": clsLogTypeItemSlow, "space_id": firstNonEmptyString(p.SpaceID, req.SpaceID), "function": p.FunctionName,
			"batch_kind": string(req.BatchKind), "request_id": req.RequestID, "dataset_id": req.DatasetID, "frequency": req.Frequency,
			"subject_id": item.Subject, "symbol": item.Symbol, "provider": item.Provider, "outcome": string(item.Outcome),
			"fetch_ms": strconv.FormatInt(item.Fetch.Milliseconds(), 10), "queue_ms": strconv.FormatInt(item.Queue.Milliseconds(), 10),
			"attempts": strconv.Itoa(item.Attempts), "http_requests": strconv.Itoa(item.HTTP.Requests),
			"conn_new": strconv.Itoa(item.HTTP.NewConns), "conn_reused": strconv.Itoa(item.HTTP.ReusedConns),
			"dns_ms": strconv.FormatInt(item.HTTP.DNSMs, 10), "connect_ms": strconv.FormatInt(item.HTTP.ConnectMs, 10),
			"tls_ms": strconv.FormatInt(item.HTTP.TLSMs, 10), "ttfb_ms": strconv.FormatInt(item.HTTP.TTFBMs, 10),
			"http_status": strconv.Itoa(item.HTTP.LastStatus), "endpoint": item.HTTP.LastDomain, "remote_addr": item.HTTP.RemoteIP,
		}
		if !closeAt.IsZero() {
			fields["since_close_start_ms"] = strconv.FormatInt(item.StartedAt.Sub(closeAt).Milliseconds(), 10)
		}
		p.Reporter.Report(clsreporter.Entry{Fields: fields})
	}
}

// reportCommit 上报批次写入 Storage 的耗时与完成时刻（相对收盘）。取数结束到写入完成之间的时间都算在这里。
func (p *KlinePipeline) reportCommit(req Request, rows int, datasets int, commit time.Duration, done, closeAt time.Time, writeErr error) {
	if p == nil || p.Reporter == nil {
		return
	}
	fields := map[string]string{
		"log_type": clsLogTypeCommit, "space_id": firstNonEmptyString(p.SpaceID, req.SpaceID), "function": p.FunctionName,
		"batch_kind": string(req.BatchKind), "request_id": req.RequestID, "dataset_id": req.DatasetID, "frequency": req.Frequency,
		"rows": strconv.Itoa(rows), "datasets": strconv.Itoa(datasets), "commit_ms": strconv.FormatInt(commit.Milliseconds(), 10),
	}
	if !closeAt.IsZero() {
		fields["since_close_done_ms"] = strconv.FormatInt(done.Sub(closeAt).Milliseconds(), 10)
	}
	if writeErr != nil {
		fields["error"] = truncateString(writeErr.Error(), clsErrorLimit)
	}
	p.Reporter.Report(clsreporter.Entry{Fields: fields})
}
