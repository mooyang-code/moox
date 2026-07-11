package hostmetrics

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
)

type fakeHostRuleSource struct {
	mu      sync.RWMutex
	rules   []domain.AlertRule
	err     error
	fetches int
}

func (s *fakeHostRuleSource) ListRules(context.Context, string) ([]domain.AlertRule, error) {
	s.mu.Lock()
	s.fetches++
	rules, err := append([]domain.AlertRule(nil), s.rules...), s.err
	s.mu.Unlock()
	return rules, err
}

func (s *fakeHostRuleSource) set(rules []domain.AlertRule, err error) {
	s.mu.Lock()
	s.rules, s.err = append([]domain.AlertRule(nil), rules...), err
	s.mu.Unlock()
}

func (s *fakeHostRuleSource) calls() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fetches
}

func hostRule(ruleID, agentID, metric string, enabled bool) domain.AlertRule {
	return domain.AlertRule{SpaceID: SpaceID, RuleID: ruleID, CheckID: HostRuleKey(agentID, metric), Enabled: enabled}
}

func TestParseHostRuleKey(t *testing.T) {
	if got := HostRuleKey("agent-1", HostMetricCPU); got != "host:agent-1:cpu" {
		t.Fatalf("key = %q", got)
	}
	agent, metric, ok := ParseHostRuleKey("host:agent-1:network_errors")
	if !ok || agent != "agent-1" || metric != HostMetricNetworkErrors {
		t.Fatalf("parse = (%q, %q, %v)", agent, metric, ok)
	}
	for _, key := range []string{"", "host::cpu", "host:agent:unknown", "service:agent:cpu", "host:agent:cpu:extra"} {
		if _, _, ok := ParseHostRuleKey(key); ok {
			t.Errorf("invalid key %q accepted", key)
		}
	}
}

func TestRuleCacheLoadsOnlyEnabledSystemHostRules(t *testing.T) {
	source := &fakeHostRuleSource{rules: []domain.AlertRule{
		hostRule("cpu-rule", "agent-1", HostMetricCPU, true),
		hostRule("disabled", "agent-1", HostMetricCPU, false),
		{SpaceID: "other", RuleID: "other", CheckID: HostRuleKey("agent-1", HostMetricCPU), Enabled: true},
		{SpaceID: SpaceID, RuleID: "service", CheckID: "check-api", Enabled: true},
	}}
	cache, err := NewRuleCache(RuleCacheOptions{Repository: source, RefreshInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := cache.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer cache.Stop(context.Background())
	rules := cache.Rules("agent-1", HostMetricCPU)
	if len(rules) != 1 || rules[0].RuleID != "cpu-rule" {
		t.Fatalf("rules = %+v", rules)
	}
	if source.calls() != 1 {
		t.Fatalf("source calls = %d, want 1", source.calls())
	}
	if _, ok := cache.Rule("agent-1", HostMetricMemory); ok {
		t.Fatal("unexpected memory rule")
	}
}

func TestRuleCacheRefreshesPeriodicallyAndKeepsPreviousOnError(t *testing.T) {
	initial := hostRule("cpu-v1", "agent-1", HostMetricCPU, true)
	source := &fakeHostRuleSource{rules: []domain.AlertRule{initial}}
	cache, err := NewRuleCache(RuleCacheOptions{Repository: source, RefreshInterval: 10 * time.Millisecond, RefreshTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cache.Stop(context.Background())

	updated := hostRule("memory-v1", "agent-1", HostMetricMemory, true)
	source.set([]domain.AlertRule{updated}, nil)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := cache.Rule("agent-1", HostMetricMemory); ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := cache.Rule("agent-1", HostMetricMemory); !ok {
		t.Fatalf("periodic refresh did not publish new rule; calls=%d", source.calls())
	}

	source.set(nil, errors.New("database unavailable"))
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) && cache.Status().LastError == "" {
		time.Sleep(time.Millisecond)
	}
	if _, ok := cache.Rule("agent-1", HostMetricMemory); !ok {
		t.Fatal("refresh failure discarded previous snapshot")
	}
	if cache.Status().LastError == "" {
		t.Fatal("refresh failure was not recorded")
	}
}

func TestRuleCacheDoesNotQuerySourceOnReads(t *testing.T) {
	source := &fakeHostRuleSource{rules: []domain.AlertRule{hostRule("cpu", "agent-1", HostMetricCPU, true)}}
	cache, err := NewRuleCache(RuleCacheOptions{Repository: source, RefreshInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer cache.Stop(context.Background())
	for i := 0; i < 100; i++ {
		_, _ = cache.Rule("agent-1", HostMetricCPU)
		_ = cache.Rules("agent-1", HostMetricCPU)
	}
	if got := source.calls(); got != 1 {
		t.Fatalf("source calls after reads = %d, want 1", got)
	}
}
