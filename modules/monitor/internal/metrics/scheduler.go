package metrics

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
)

type RuleScheduler struct {
	evaluator *MetricEvaluator
	rules     *RuleRepository
	instance  string
	peers     func(context.Context) ([]string, error)
	interval  time.Duration
	stop      chan struct{}
	once      sync.Once
	wg        sync.WaitGroup
}
type SchedulerOptions struct {
	Evaluator       *MetricEvaluator
	Rules           *RuleRepository
	InstanceID      string
	ReloadInterval  time.Duration
	ActiveInstances func(context.Context) ([]string, error)
}

func NewRuleScheduler(opts SchedulerOptions) *RuleScheduler {
	d := opts.ReloadInterval
	if d <= 0 {
		d = time.Minute
	}
	return &RuleScheduler{evaluator: opts.Evaluator, rules: opts.Rules, instance: opts.InstanceID, peers: opts.ActiveInstances, interval: d, stop: make(chan struct{})}
}
func (s *RuleScheduler) Start(ctx context.Context) {
	if s == nil || s.evaluator == nil || s.rules == nil {
		return
	}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.run(ctx) }()
}
func (s *RuleScheduler) Stop() {
	if s == nil {
		return
	}
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
}
func (s *RuleScheduler) run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.evaluate(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticker.C:
			s.evaluate(ctx)
		}
	}
}
func (s *RuleScheduler) evaluate(ctx context.Context) {
	rules, err := s.rules.ListEnabled(ctx, "")
	if err != nil {
		return
	}
	ids := []string{s.instance}
	if s.peers != nil {
		if got, e := s.peers(ctx); e == nil && len(got) > 0 {
			ids = got
		}
	}
	sort.Strings(ids)
	for _, rule := range rules {
		if owner := alerting.Owner("metric", rule.GetRuleId(), ids); owner != "" && owner != s.instance {
			continue
		}
		_, _ = s.evaluator.Evaluate(ctx, rule, false)
	}
}
