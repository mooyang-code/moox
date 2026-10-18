package bootstrap

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
)

func TestEvaluationFreshness(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	sched := scheduler.New(nil, scheduler.Options{})

	if ready, _ := evaluationFresh(&Runtime{StartedAt: now.Add(-time.Minute), Scheduler: sched}, now); !ready {
		t.Fatal("刚启动 3 分钟内不判断评估停滞")
	}
	if ready, reason := evaluationFresh(&Runtime{StartedAt: now.Add(-10 * time.Minute), Scheduler: sched}, now); ready || reason == "" {
		t.Fatal("启动 10 分钟仍没有完成过一次评估应判为未就绪")
	}
	if ready, _ := evaluationFresh(&Runtime{StartedAt: now.Add(-10 * time.Minute)}, now); ready {
		t.Fatal("没有调度器应判为未就绪")
	}
}
