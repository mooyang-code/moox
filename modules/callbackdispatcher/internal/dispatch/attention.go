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
//   - action_failed：系统动作的记录失败，直到用户处理；
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
	items[telemetry.AttentionDelistDetected] = telemetry.Recent(telemetry.AttentionDelistDetected, a.now(), telemetry.DelistDetectedWindow)
	telemetry.PublishAttention(items)
	return nil
}
