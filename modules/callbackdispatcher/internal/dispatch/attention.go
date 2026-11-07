package dispatch

import (
	"context"
	"regexp"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/telemetry"
	"trpc.group/trpc-go/trpc-go/log"
)

var urlPattern = regexp.MustCompile(`"?https?://[^\s"]+"?:?`)

// stableReason 去掉错误里的地址并截短：错误原文可能带签名参数，每次都不同，会让指标序列不断增长。
func stableReason(reason string) string {
	reason = urlPattern.ReplaceAllString(reason, "")
	if runes := []rune(reason); len(runes) > 60 {
		reason = string(runes[:60])
	}
	return reason
}

// AttentionWorker 每轮从库里的状态重新算出关注项并上报，重启不丢：
//   - execution_failed：通知类的记录失败，直到用户重新执行或确认；
//   - action_failed：系统动作的记录失败，直到用户处理；已取消但取消后的善后（解除禁止买入）没做成的记录也算，直到再取消一次做成；
//     还没结束的记录里有下架步骤失败（某个组合的平仓用完重试、到了下架时间仍没平完、只平了一部分）也算，直到记录结束；
//     下架后核对时留有零头只报一次（telemetry.Note），约 3 分钟；
//   - delist_detected：由系统动作处理器记下（telemetry.NoteDelistDetected），约 3 分钟。
type AttentionWorker struct {
	Store    *store.Store
	Interval time.Duration
	Now      func() time.Time
}

func (a *AttentionWorker) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Run 一直上报到 ctx 结束。
func (a *AttentionWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(a.Interval)
	defer ticker.Stop()
	for {
		if err := a.Publish(ctx); err != nil && ctx.Err() == nil {
			log.WarnContextf(ctx, "计算事件中心关注项失败: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Publish 计算并上报一次。
func (a *AttentionWorker) Publish(ctx context.Context) error {
	failures, err := a.Store.ListUnacknowledgedFailures(ctx)
	if err != nil {
		return err
	}
	items := map[string][]string{}
	for _, execution := range failures {
		kind := telemetry.AttentionExecutionFailed
		if execution.ActionType == store.ActionSystem {
			kind = telemetry.AttentionActionFailed
		}
		items[kind] = append(items[kind], execution.RuleName+"："+stableReason(execution.LastError))
	}
	cleanups, err := a.Store.ListPendingCleanups(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	// 整条记录的成败只在下架后核对时决定，某个组合的某一轮平仓失败不会让记录失败（后面的轮次还要继续），所以单独提醒。
	stepFailures, err := a.Store.ListUnfinishedWithFailedSteps(ctx)
	if err != nil {
		return err
	}
	for _, execution := range stepFailures {
		detail := execution.RuleName + "（" + execution.SubjectID + "）：下架处理有步骤失败或只完成一部分，请在执行记录里看各步结果"
		if !seen[detail] {
			seen[detail] = true
			items[telemetry.AttentionActionFailed] = append(items[telemetry.AttentionActionFailed], detail)
		}
	}
	for _, execution := range cleanups {
		// 带上标的，并去重：同一规则、同一标的多条善后未完成的记录只报一次，不同标的各报一条。
		detail := execution.RuleName + "（" + execution.SubjectID + "）：取消后的善后未完成，请在执行记录里点“重试善后”"
		if !seen[detail] {
			seen[detail] = true
			items[telemetry.AttentionActionFailed] = append(items[telemetry.AttentionActionFailed], detail)
		}
	}
	items[telemetry.AttentionActionFailed] = append(items[telemetry.AttentionActionFailed], telemetry.Recent(telemetry.AttentionActionFailed, a.now(), telemetry.DelistDetectedWindow)...)
	items[telemetry.AttentionDelistDetected] = telemetry.Recent(telemetry.AttentionDelistDetected, a.now(), telemetry.DelistDetectedWindow)
	telemetry.PublishAttention(items)
	return nil
}
