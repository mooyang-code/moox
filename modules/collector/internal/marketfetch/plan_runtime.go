package marketfetch

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"trpc.group/trpc-go/trpc-go/log"
)

// PlanRunResult 是一个频率一次执行的结果摘要。
type PlanRunResult struct {
	Frequency string `json:"frequency"`
	Period    string `json:"period"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
}

// HandlePlanTimer 处理定时触发：按环境里的采集计划与事件计划时间，对到期的频率各执行一次，
// 把结果直接提交给 Storage。不向 Collector 领取批次，不发布完成事件，不登记周期。
// scheduled 必须是事件携带的计划时间，调用方已拒绝缺失或非法的时间。
func (h *Handler) HandlePlanTimer(ctx context.Context, requestID, functionName string, scheduled time.Time) (*model.Response, error) {
	if h == nil {
		return nil, fmt.Errorf("market fetch handler is nil")
	}
	respond := func(success bool, message string, results []PlanRunResult) (*model.Response, error) {
		return &model.Response{Success: success, Message: message, Data: results, RequestID: requestID, Timestamp: time.Now().UTC()}, nil
	}
	if scheduled.IsZero() {
		return respond(false, "timer scheduled time is required", nil)
	}
	spaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
	if spaceID == "" {
		return respond(false, "MOOX_SPACE_ID is required", nil)
	}
	plan, err := DecodePlan(os.Getenv(PlanEnvName))
	if err != nil {
		return respond(false, err.Error(), nil)
	}
	applyPlanFetchEnvironment(plan.Fetch)
	dnsRoutes, err := parseDNSRoutes(os.Getenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON"))
	if err != nil {
		return respond(false, err.Error(), nil)
	}
	planned, err := plan.BuildRequests(spaceID, functionName, requestID, scheduled, dnsRoutes)
	if err != nil {
		return respond(false, err.Error(), nil)
	}
	if h.PlanPeriodValid != nil {
		kept := planned[:0]
		for _, item := range planned {
			if h.PlanPeriodValid(plan.Market, item.Frequency, item.Period) {
				kept = append(kept, item)
			}
		}
		planned = kept
	}
	if len(planned) == 0 {
		return respond(true, "no_work", nil)
	}
	hedgeMS := plan.HedgeFor(functionName, scheduled)
	for index := range planned {
		planned[index].Request.HedgeMS = hedgeMS
	}
	// 截止时间最近的先执行：整点同时到期多个频率时，短周期不能被长周期占去预算。
	// 截止时间相同则周期短的先执行（1m 先于 1h）。
	sort.SliceStable(planned, func(i, j int) bool {
		if !planned[i].StorageDeadline.Equal(planned[j].StorageDeadline) {
			return planned[i].StorageDeadline.Before(planned[j].StorageDeadline)
		}
		return frequencyDuration(planned[i].Frequency) < frequencyDuration(planned[j].Frequency)
	})
	invocationHandler, closeGateway, err := h.prepareInvocation()
	if err != nil {
		return respond(false, err.Error(), nil)
	}
	defer closeGateway()
	defer h.reportMetrics(ctx)
	if delay := plan.StaggerDelay(functionName); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return respond(false, ctx.Err().Error(), nil)
		}
	}
	ctx = gatewayclient.WithCallMetadata(ctx, gatewayclient.CallMetadata{SpaceID: spaceID, TraceID: requestID})
	results := make([]PlanRunResult, 0, len(planned))
	allSucceeded := true
	for _, item := range planned {
		result := PlanRunResult{Frequency: item.Frequency, Period: item.Period.Format(time.RFC3339)}
		now := time.Now()
		if h.Now != nil {
			now = h.Now()
		}
		if !now.Before(item.StorageDeadline) {
			// 事件迟到到周期截止之后：Storage 会拒绝，数据交给补采，这里不再抓取。
			result.Status, result.Message = "expired", "event arrived after the period deadline"
			results = append(results, result)
			allSucceeded = false
			continue
		}
		runCtx, cancel := context.WithDeadline(ctx, item.StorageDeadline)
		closeAt := item.Period.Add(frequencyDuration(item.Frequency))
		runStarted := now
		response, runErr := invocationHandler.handleRequest(runCtx, item.Request)
		cancel()
		// 记录相对收盘时间的起止与截止，便于看出行情源延迟尖峰是否吃光了窗口。
		finished := time.Now()
		if h.Now != nil {
			finished = h.Now()
		}
		timing := fmt.Sprintf(" (start=%+dms end=%+dms deadline=%+dms after close)", runStarted.Sub(closeAt).Milliseconds(), finished.Sub(closeAt).Milliseconds(), item.StorageDeadline.Sub(closeAt).Milliseconds())
		switch {
		case runErr != nil:
			result.Status, result.Message = "failed", runErr.Error()
			allSucceeded = false
		case response == nil || !response.Success:
			result.Status = "failed"
			if response != nil {
				result.Message = response.Message + summarizeBatchFailures(response.Data) + timing
			}
			allSucceeded = false
		default:
			result.Status, result.Message = "succeeded", response.Message+summarizeBatchFailures(response.Data)+timing
			if response.Message == "partial_failed" {
				result.Status = "partial_failed"
				allSucceeded = false
			}
		}
		log.InfoContextf(ctx, "market_fetch_plan_run function=%s frequency=%s period=%s status=%s items=%d targets=%d message=%s",
			functionName, result.Frequency, result.Period, result.Status, len(item.Request.Items), len(item.Request.Targets), result.Message)
		results = append(results, result)
	}
	message := "succeeded"
	if !allSucceeded {
		message = "partial_failed"
	}
	return respond(allSucceeded, message, results)
}

// applyPlanFetchEnvironment 把计划里的拉取参数设置为进程环境，供数据源适配器在每次调用时读取。
// 一个函数只服务一个市场，计划本身固定，所以进程级设置是确定的。
func applyPlanFetchEnvironment(fetch PlanFetch) {
	set := func(name string, value int) {
		if value > 0 {
			_ = os.Setenv(name, strconv.Itoa(value))
		}
	}
	set("MOOX_FETCH_RPS", fetch.RPS)
	set("MOOX_FETCH_BURST", fetch.Burst)
	set("MOOX_FETCH_FEED_CONCURRENCY", fetch.FeedConcurrency)
	set("MOOX_FETCH_REQUEST_TIMEOUT_MS", fetch.RequestTimeoutMS)
	set("MOOX_FETCH_STORAGE_TIMEOUT_MS", fetch.StorageTimeoutMS)
}

// summarizeBatchFailures 把批次里失败条目的错误按（类型、摘要）归并，取出现最多的几类，
// 写进返回值与日志，方便从一次调用的结果直接看出失败原因。没有失败时返回空串。
func summarizeBatchFailures(data any) string {
	payload, ok := data.(*marketfetchpb.MarketFetchBatchCompleted)
	if !ok || payload == nil {
		return ""
	}
	counts := map[string]int{}
	for _, item := range payload.GetItems() {
		if item.GetOutcome() == string(domain.ItemOutcomeSuccess) {
			continue
		}
		summary := item.GetErrorSummary()
		if len(summary) > 120 {
			summary = summary[:120]
		}
		counts[item.GetOutcome()+"/"+item.GetErrorType()+": "+summary]++
	}
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > 3 {
		keys = keys[:3]
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s ×%d", key, counts[key]))
	}
	return " [" + strings.Join(parts, "; ") + "]"
}

// frequencyDuration 返回频率的名义时长，仅用于诊断信息里的相对时间。
func frequencyDuration(name string) time.Duration {
	frequency, err := marketdata.ParseFrequency(name)
	if err != nil {
		return 0
	}
	return frequency.NominalDuration()
}
