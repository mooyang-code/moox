package metrics

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

type RuleScheduler struct {
	evaluator *MetricEvaluator
	rules     *RuleRepository
	instance  string
	peers     func(context.Context) ([]string, error)
	interval  time.Duration
	now       func() time.Time
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
	Now             func() time.Time
}

func NewRuleScheduler(opts SchedulerOptions) *RuleScheduler {
	d := opts.ReloadInterval
	if d <= 0 {
		d = time.Minute
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RuleScheduler{evaluator: opts.Evaluator, rules: opts.Rules, instance: opts.InstanceID, peers: opts.ActiveInstances, interval: d, now: now, stop: make(chan struct{})}
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
	// Rules are paged in bounded chunks. A malformed or unexpectedly large
	// catalog must not turn one scheduler tick into an unbounded query/loop.
	const pageSize, maxRules = 500, 10000
	var rules []*monitorpb.MetricRule
	for offset := 0; offset < maxRules; offset += pageSize {
		page, total, err := s.rules.ListRules(ctx, "", true, offset, pageSize)
		if err != nil {
			return
		}
		rules = append(rules, page...)
		if len(page) < pageSize || int64(offset+len(page)) >= total {
			break
		}
	}
	ids := []string{s.instance}
	if s.peers != nil {
		if got, e := s.peers(ctx); e == nil && len(got) > 0 {
			ids = got
		}
	}
	sort.Strings(ids)
	now := s.now()
	for _, rule := range rules {
		if owner := alerting.Owner("metric", rule.GetRuleId(), ids); owner != "" && owner != s.instance {
			continue
		}
		state, err := s.rules.GetState(ctx, rule.GetSpaceId(), rule.GetRuleId())
		if err == nil && state.LastEvaluatedAt != nil && rule.GetEvaluationIntervalSeconds() > 0 && now.Sub(*state.LastEvaluatedAt) < time.Duration(rule.GetEvaluationIntervalSeconds())*time.Second {
			continue
		}
		_, _ = s.evaluator.Evaluate(ctx, rule, false)
	}
}
