package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/notifier"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/notification"
	"trpc.group/trpc-go/trpc-go/log"
)

// 告警静默（暂停，包括 moox-cli monitor mute 设置的屏蔽）期间告警只记录、不推送。静默结束时补发一条汇总，
// 免得真问题被吞掉：仍未恢复的告警逐条列出，期间触发后又恢复的按检查项列出次数与时间范围。

const (
	muteSummaryEventLimit = 2000
	// 触发中的告警数量天然受检查项总数约束（几百），上限只是兜底；取不全时「已恢复」的分类可能不准，正文会写明。
	muteSummaryFiringLimit = 5000
	muteSummaryLineLimit   = 20
	// muteSummaryFallbackWindow 是重启后丢了静默起点时回看的范围（一次屏蔽最长 24 小时）。
	muteSummaryFallbackWindow = 24 * time.Hour
)

// muteTracker 记录模块进入静默的时间，静默结束时据此汇总。
type muteTracker struct{ since atomic.Int64 }

func (t *muteTracker) enter(at time.Time) {
	// 已经在静默中（暂停 → 恢复追赶 → 暂停）保留最早的起点。
	t.since.CompareAndSwap(0, at.UnixNano())
}

func (t *muteTracker) leave() time.Time {
	nanos := t.since.Swap(0)
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos).UTC()
}

// onLifecycleChange 是 Monitor 生命周期的阶段变化回调：进入暂停时记起点，回到放行阶段时补发汇总。
func (r *Runtime) onLifecycleChange(ref lifecycle.Ref, from, to lifecycle.Stage, reason string) {
	log.Infof("monitor_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
	if ref != (lifecycle.Ref{}) {
		return
	}
	silenced := func(stage lifecycle.Stage) bool {
		return stage == lifecycle.StagePaused || stage == lifecycle.StageResuming
	}
	switch {
	case to == lifecycle.StagePaused:
		// 起点取生命周期持久化的暂停时间：Monitor 在静默期间重启时，回调里的「现在」是重启时刻，会丢掉重启前的事件。
		at := time.Now()
		if life := r.life.Load(); life != nil {
			if since := life.Snapshot().Self.PausedSince; !since.IsZero() {
				at = since
			}
		}
		r.mutes.enter(at)
	case silenced(from) && !silenced(to) && to.Active():
		since := r.mutes.leave()
		if since.IsZero() {
			since = time.Now().Add(-muteSummaryFallbackWindow)
		}
		r.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := r.sendMuteSummary(ctx, since, time.Now()); err != nil {
				log.Errorf("monitor mute summary failed: %v", err)
			}
		})
	}
}

// sendMuteSummary 在静默结束后补发汇总；没有值得说的内容时不发。
func (r *Runtime) sendMuteSummary(ctx context.Context, since, until time.Time) error {
	if r == nil || r.Repositories == nil {
		return nil
	}
	body, severity, ok, err := r.buildMuteSummary(ctx, since, until)
	if err != nil || !ok {
		return err
	}
	sender, err := notifier.Sender(ctx, r.Repositories.Notifications)
	if err != nil {
		return err
	}
	if sender == nil {
		return nil
	}
	return sender.Send(ctx, notification.Message{
		Key:      "monitor-mute-summary",
		Severity: severity,
		Title:    "告警静默结束",
		Body:     body,
	})
}

type mutedCheck struct {
	name       string
	triggers   int
	resolved   int
	first, end time.Time
}

func (r *Runtime) buildMuteSummary(ctx context.Context, since, until time.Time) (string, notification.Severity, bool, error) {
	events, err := r.Repositories.Alerts.ListEventsSince(ctx, nil, since, muteSummaryEventLimit)
	if err != nil {
		return "", "", false, err
	}
	firing, err := r.Repositories.Alerts.ListFiringStates(ctx, "", muteSummaryFiringLimit)
	if err != nil {
		return "", "", false, err
	}
	// 事件和触发态都有上限：达到上限时汇总只覆盖最近的部分，在正文里写明，不能悄悄少报。
	truncated := len(events) >= muteSummaryEventLimit || len(firing) >= muteSummaryFiringLimit
	stillFiring := make(map[string]bool, len(firing))
	for _, state := range firing {
		stillFiring[state.SpaceID+"/"+state.CheckID] = true
	}
	byCheck := map[string]*mutedCheck{}
	label := func(spaceID, checkID string) string {
		if check, err := r.Repositories.Checks.Get(ctx, spaceID, checkID); err == nil && check != nil && check.Name != "" {
			return check.Name
		}
		return checkID
	}
	for _, event := range events {
		if event.EventType != domain.AlertEventTriggered && event.EventType != domain.AlertEventResolved {
			continue
		}
		key := event.SpaceID + "/" + event.CheckID
		item := byCheck[key]
		if item == nil {
			item = &mutedCheck{name: label(event.SpaceID, event.CheckID), first: event.CreatedAt}
			byCheck[key] = item
		}
		item.end = event.CreatedAt
		if event.EventType == domain.AlertEventTriggered {
			item.triggers++
		} else {
			item.resolved++
		}
	}
	var open, recovered []string
	keys := make([]string, 0, len(byCheck))
	for key := range byCheck {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		item := byCheck[key]
		if stillFiring[key] {
			open = append(open, fmt.Sprintf("%s（触发 %d 次，最近 %s）", item.name, item.triggers, alerttext.Time(item.end)))
		} else {
			recovered = append(recovered, fmt.Sprintf("%s（触发 %d 次，恢复 %d 次，%s ～ %s）", item.name, item.triggers, item.resolved, alerttext.Time(item.first), alerttext.Time(item.end)))
		}
	}
	// 屏蔽前就已在触发、期间没有新事件的告警也属于「仍未恢复」，不能漏。
	for _, state := range firing {
		key := state.SpaceID + "/" + state.CheckID
		if byCheck[key] == nil {
			open = append(open, fmt.Sprintf("%s（屏蔽前已在触发）", label(state.SpaceID, state.CheckID)))
		}
	}
	if len(open) == 0 && len(recovered) == 0 {
		return "", "", false, nil
	}
	var body strings.Builder
	fmt.Fprintf(&body, "静默时段：%s ～ %s（北京时间）\n", alerttext.Time(since), alerttext.Time(until))
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&body, "\n%s（%d 项）：\n", title, len(lines))
		for i, line := range lines {
			if i == muteSummaryLineLimit {
				fmt.Fprintf(&body, "……另有 %d 项\n", len(lines)-i)
				break
			}
			body.WriteString("· " + line + "\n")
		}
	}
	section("仍未恢复", open)
	section("期间触发后已恢复", recovered)
	if truncated {
		fmt.Fprintf(&body, "\n注意：静默期间的告警事件太多，上面只统计了最近 %d 条事件、%d 个触发中的告警，更早的以告警记录为准；触发中的告警取不全时，上面“已恢复”的分类也可能不准。\n", muteSummaryEventLimit, muteSummaryFiringLimit)
	}
	severity := notification.SeverityInfo
	if len(open) > 0 {
		severity = notification.SeverityWarning
	}
	return strings.TrimRight(body.String(), "\n"), severity, true, nil
}
