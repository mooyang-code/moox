package checks

import (
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
)

// Step 是告警推进的纯函数：输入上一轮的检查行（新检查为零值）、本轮事实、规则与当前时间，
// 返回新的检查行和要写的事件类型（triggered、reminder、resolved，可能为空）。
//   - 失败：失败计数加 1、成功计数清零、记下失败原因与时间；未告警且失败计数达到阈值即触发；
//     已告警且上次推送没送达（NotifiedAt 为空）或距今不少于提醒间隔即提醒。
//   - 通过（ok、degraded）：成功计数加 1、失败计数清零；告警中且成功计数达到阈值即恢复。
func Step(prev domain.Check, fact Fact, rule Rule, now time.Time) (domain.Check, []string) {
	next := prev
	next.SpaceID, next.CheckID, next.Name = fact.SpaceID, fact.CheckID, fact.Name
	next.Severity = fact.Severity
	if next.Severity == "" {
		next.Severity = Severity(fact.CheckID)
	}
	next.Status, next.Reason, next.RawError, next.CheckedAt = fact.Status, fact.Reason, fact.RawError, now
	if next.AlertStatus == "" {
		next.AlertStatus = domain.AlertStatusOK
	}
	if next.CreatedAt.IsZero() {
		next.CreatedAt = now
	}
	var events []string
	if !fact.Passed() {
		next.FailureCount++
		next.SuccessCount = 0
		failedAt := now
		next.FailureReason, next.FailureRawError, next.FailedAt = fact.Reason, fact.RawError, &failedAt
		switch {
		case !next.Firing() && next.FailureCount >= rule.Failures:
			triggered := now
			next.AlertStatus, next.TriggeredAt, next.NotifiedAt = domain.AlertStatusFiring, &triggered, &triggered
			events = append(events, domain.AlertEventTriggered)
		case next.Firing() && (next.NotifiedAt == nil || now.Sub(*next.NotifiedAt) >= rule.Reminder):
			notified := now
			next.NotifiedAt = &notified
			events = append(events, domain.AlertEventReminder)
		}
		return next, events
	}
	next.SuccessCount++
	next.FailureCount = 0
	if next.Firing() && next.SuccessCount >= rule.Successes {
		next.AlertStatus = domain.AlertStatusOK
		events = append(events, domain.AlertEventResolved)
	}
	return next, events
}
