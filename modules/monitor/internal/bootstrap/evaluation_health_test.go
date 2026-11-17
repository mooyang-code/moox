package bootstrap

import (
	"testing"
	"time"
)

func TestEvaluationFreshness(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	if ready, _ := evaluationFresh(&Runtime{StartedAt: now.Add(-time.Minute)}, now); !ready {
		t.Fatal("刚启动 3 分钟内不判断评估停滞")
	}
	runtime := &Runtime{StartedAt: now.Add(-10 * time.Minute)}
	if ready, reason := evaluationFresh(runtime, now); ready || reason == "" {
		t.Fatal("启动 10 分钟仍没有完成过一次评估应判为未就绪")
	}
	runtime.checkRoundAt.Store(now.Add(-4 * time.Minute).UnixNano())
	if ready, _ := evaluationFresh(runtime, now); ready {
		t.Fatal("检查定时器 4 分钟没有完整成功一轮应判为未就绪")
	}
	runtime.checkRoundAt.Store(now.Add(-time.Minute).UnixNano())
	if ready, _ := evaluationFresh(runtime, now); !ready {
		t.Fatal("检查定时器 1 分钟前成功过一轮应判为就绪")
	}
	if ready, _ := evaluationFresh(nil, now); ready {
		t.Fatal("没有运行时应判为未就绪")
	}
}

// 业务评估连续失败（或启动后从没成功过）也判为未就绪，即使主机类检查那一步一直成功。
func TestEvaluationFreshnessCoversBusinessEvaluation(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	runtime := &Runtime{StartedAt: now.Add(-10 * time.Minute), businessEvaluation: true}
	if ready, reason := businessEvaluationFresh(runtime, now); ready || reason == "" {
		t.Fatal("业务评估从没成功过应判为未就绪")
	}
	runtime.businessEvaluatedAt.Store(now.Add(-4 * time.Minute).UnixNano())
	if ready, _ := businessEvaluationFresh(runtime, now); ready {
		t.Fatal("业务评估 4 分钟没有成功应判为未就绪")
	}
	runtime.businessEvaluatedAt.Store(now.Add(-time.Minute).UnixNano())
	if ready, _ := businessEvaluationFresh(runtime, now); !ready {
		t.Fatal("业务评估 1 分钟前成功过应判为就绪")
	}
	runtime.businessEvaluation = false
	runtime.businessEvaluatedAt.Store(0)
	if ready, _ := businessEvaluationFresh(runtime, now); !ready {
		t.Fatal("没有业务评估时不判断")
	}
}
