package checks

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
)

func TestStep(t *testing.T) {
	start := time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	rule := Rule{Failures: 3, Successes: 2, Reminder: 5 * time.Minute}
	down := Fact{SpaceID: "s", CheckID: "c", Name: "检查", Status: domain.CheckStatusDown, Reason: "坏了"}
	ok := Fact{SpaceID: "s", CheckID: "c", Name: "检查", Status: domain.CheckStatusOK, Reason: "正常"}
	degraded := Fact{SpaceID: "s", CheckID: "c", Name: "检查", Status: domain.CheckStatusDegraded, Reason: "需关注"}
	type step struct {
		fact   Fact
		after  time.Duration
		events []string
		firing bool
	}
	run := func(t *testing.T, row domain.Check, steps []step) domain.Check {
		t.Helper()
		for i, s := range steps {
			var events []string
			row, events = Step(row, s.fact, rule, start.Add(s.after))
			if len(events) != len(s.events) || (len(events) > 0 && events[0] != s.events[0]) {
				t.Fatalf("第 %d 步事件 %v，应为 %v", i+1, events, s.events)
			}
			if row.Firing() != s.firing {
				t.Fatalf("第 %d 步告警状态 %s", i+1, row.AlertStatus)
			}
		}
		return row
	}

	t.Run("达到失败阈值才触发、按间隔提醒、恢复要连续通过", func(t *testing.T) {
		row := run(t, domain.Check{}, []step{
			{down, 0, nil, false},
			{down, 30 * time.Second, nil, false},
			{down, time.Minute, []string{domain.AlertEventTriggered}, true},
			{down, 4 * time.Minute, nil, true},
			{down, 6 * time.Minute, []string{domain.AlertEventReminder}, true},
			// 告警中间夹一次通过：不恢复，也不清失败原因。
			{ok, 7 * time.Minute, nil, true},
			{down, 8 * time.Minute, nil, true},
			{ok, 9 * time.Minute, nil, true},
		})
		if row.FailureReason != "坏了" || row.FailedAt == nil {
			t.Fatalf("恢复确认期间应保留失败原因: %+v", row)
		}
		row = run(t, row, []step{{ok, 10 * time.Minute, []string{domain.AlertEventResolved}, false}})
		if row.Severity != domain.SeverityCritical || row.Name != "检查" || !row.CreatedAt.Equal(start) {
			t.Fatalf("名称、级别、首次出现时间不对: %+v", row)
		}
	})

	t.Run("上次推送没送达时下一轮重发", func(t *testing.T) {
		row := run(t, domain.Check{}, []step{{down, 0, nil, false}, {down, 30 * time.Second, nil, false}, {down, time.Minute, []string{domain.AlertEventTriggered}, true}})
		row.NotifiedAt = nil
		run(t, row, []step{{down, 90 * time.Second, []string{domain.AlertEventReminder}, true}})
	})

	t.Run("degraded 按通过记录", func(t *testing.T) {
		row := run(t, domain.Check{}, []step{{degraded, 0, nil, false}, {degraded, time.Minute, nil, false}, {degraded, 2 * time.Minute, nil, false}})
		if row.Status != domain.CheckStatusDegraded || row.SuccessCount != 3 || row.FailureCount != 0 {
			t.Fatalf("degraded 应计为通过: %+v", row)
		}
	})

	t.Run("事实给出的级别覆盖按检查 ID 推出的级别", func(t *testing.T) {
		fact := down
		fact.Severity = domain.SeverityWarning
		row, _ := Step(domain.Check{}, fact, rule, start)
		if row.Severity != domain.SeverityWarning {
			t.Fatalf("级别应取事实里的: %s", row.Severity)
		}
	})
}
