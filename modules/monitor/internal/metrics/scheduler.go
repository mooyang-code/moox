package metrics

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

type RuleScheduler struct {
	evaluator *MetricEvaluator
	rules     *MetricRuleStore
	instance  string
	peers     func(context.Context) ([]string, error)
	now       func() time.Time
}
type SchedulerOptions struct {
	Evaluator       *MetricEvaluator
	Rules           *MetricRuleStore
	InstanceID      string
	ActiveInstances func(context.Context) ([]string, error)
	Now             func() time.Time
}

func NewRuleScheduler(opts SchedulerOptions) *RuleScheduler {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RuleScheduler{evaluator: opts.Evaluator, rules: opts.Rules, instance: opts.InstanceID, peers: opts.ActiveInstances, now: now}
}

// EvaluateDueOnce evaluates one bounded snapshot of metric rules.
func (s *RuleScheduler) EvaluateDueOnce(ctx context.Context) error {
	if s == nil || s.evaluator == nil || s.rules == nil {
		return nil
	}
	// Rules are paged in bounded chunks. A malformed or unexpectedly large
	// catalog must not turn one scheduler tick into an unbounded query/loop.
	const pageSize, maxRules = 500, 10000
	var rules []*monitorpb.MetricRule
	for offset := 0; offset < maxRules; offset += pageSize {
		page, total, err := s.rules.ListRules(ctx, "", true, offset, pageSize)
		if err != nil {
			return err
		}
		rules = append(rules, page...)
		if len(page) < pageSize || int64(offset+len(page)) >= total {
			break
		}
	}
	ids := []string{s.instance}
	if s.peers != nil {
		got, err := s.peers(ctx)
		if err != nil {
			return err
		}
		if len(got) > 0 {
			ids = got
		}
	}
	sort.Strings(ids)
	now := s.now()
	var errs []error
	for _, rule := range rules {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if owner := alerting.Owner("metric", rule.GetRuleId(), ids); owner != "" && owner != s.instance {
			continue
		}
		state, err := s.rules.GetState(ctx, rule.GetSpaceId(), rule.GetRuleId())
		if err == nil && state.LastEvaluatedAt != nil && rule.GetEvaluationIntervalSeconds() > 0 && now.Sub(*state.LastEvaluatedAt) < time.Duration(rule.GetEvaluationIntervalSeconds())*time.Second {
			continue
		}
		if _, err := s.evaluator.Evaluate(ctx, rule, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
