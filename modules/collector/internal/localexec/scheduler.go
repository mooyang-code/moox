package localexec

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/stream"
	"trpc.group/trpc-go/trpc-go/log"
)

// Scheduler 是本地执行端的定时拉取：ingest=pull、executor=local 的数据集由它按收盘时刻触发，
// 计划从 PullStreamPlan 取（与推送分配同一条下发通道），不经过云定时器与函数环境变量。
type Scheduler struct {
	executor *Executor
	mu       sync.Mutex
	plans    map[string]*marketfetch.Plan
}

// NewScheduler 构造定时拉取；执行占用本地执行端的并发名额。
func NewScheduler(executor *Executor) *Scheduler {
	return &Scheduler{executor: executor, plans: map[string]*marketfetch.Plan{}}
}

// Apply 换成新的本地拉取计划（只取执行端为 local 的项）。
func (s *Scheduler) Apply(items []stream.PlanItem) {
	plans := map[string]*marketfetch.Plan{}
	for _, item := range items {
		if item.Executor == marketfetch.PlanExecutorLocal && item.Plan != nil {
			plans[strings.ToLower(item.Plan.Market.MarketType)] = item.Plan
		}
	}
	s.mu.Lock()
	s.plans = plans
	s.mu.Unlock()
}

// Run 每分钟收盘时刻对每个市场执行一次计划（到期的频率由计划按触发时间判断）。
func (s *Scheduler) Run(ctx context.Context) {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.mu.Lock()
		plans := make(map[string]*marketfetch.Plan, len(s.plans))
		for market, plan := range s.plans {
			plans[market] = plan
		}
		s.mu.Unlock()
		for market, plan := range plans {
			go s.run(ctx, market, plan, next)
		}
	}
}

func (s *Scheduler) run(ctx context.Context, market string, plan *marketfetch.Plan, scheduled time.Time) {
	response, err := s.executor.RunPlan(ctx, plan, marketfetch.PlanRun{
		RequestID:    FunctionName(s.executor.spaceID) + "-" + market + "-" + scheduled.UTC().Format("200601021504"),
		FunctionName: FunctionName(s.executor.spaceID) + "-" + market, Scheduled: scheduled, Entry: time.Now(),
	})
	switch {
	case err != nil:
		log.Warnf("local executor pull market=%s scheduled=%s failed: %v", market, scheduled.Format(time.RFC3339), err)
	case response != nil && !response.Success:
		log.Warnf("local executor pull market=%s scheduled=%s: %s", market, scheduled.Format(time.RFC3339), response.Message)
	}
}
