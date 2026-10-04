package hostmetrics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/snapshotcache"
)

// Host rule keys are stored in the existing alert-rule check_id column.  The
// prefix keeps host rules separate from ordinary service health checks while
// allowing one alert-rule repository and one SQLite control plane.
const HostRulePrefix = "host:"

const (
	HostMetricCPU                = "cpu"
	HostMetricMemory             = "memory"
	HostMetricFilesystemUsage    = "filesystem_usage"
	HostMetricDiskUtilization    = "disk_utilization"
	HostMetricNetworkErrors      = "network_errors"
	hostRuleIndex                = "host_rule_key"
	hostRuleIDIndex              = "rule_id"
	defaultHostRuleRefreshPeriod = 30 * time.Second
)

// HostRuleKey returns the stable check_id used by host alert rules.
func HostRuleKey(agentID, metric string) string {
	return HostRulePrefix + strings.TrimSpace(agentID) + ":" + strings.TrimSpace(metric)
}

// ParseHostRuleKey validates and splits a host alert rule key.
func ParseHostRuleKey(key string) (agentID, metric string, ok bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 3 || parts[0] != strings.TrimSuffix(HostRulePrefix, ":") {
		return "", "", false
	}
	if strings.TrimSpace(parts[1]) == "" || !isHostMetric(parts[2]) {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func isHostMetric(metric string) bool {
	switch metric {
	case HostMetricCPU, HostMetricMemory, HostMetricFilesystemUsage, HostMetricDiskUtilization, HostMetricNetworkErrors:
		return true
	default:
		return false
	}
}

// HostRuleSource is the small repository surface needed by the periodic
// cache.  store.AlertRepository satisfies it without coupling this
// package to GORM or to the concrete repository implementation.
type HostRuleSource interface {
	ListRules(context.Context, string) ([]domain.AlertRule, error)
}

type hostRuleSource struct{ repository HostRuleSource }

func (s hostRuleSource) Fetch(ctx context.Context) ([]domain.AlertRule, error) {
	if s.repository == nil {
		return nil, errors.New("host alert rule repository is nil")
	}
	rules, err := s.repository.ListRules(ctx, SpaceID)
	if err != nil {
		return nil, err
	}
	filtered := make([]domain.AlertRule, 0, len(rules))
	for _, rule := range rules {
		if rule.SpaceID != SpaceID || !rule.Enabled {
			continue
		}
		if _, _, ok := ParseHostRuleKey(rule.CheckID); !ok {
			continue
		}
		filtered = append(filtered, cloneHostRule(rule))
	}
	return filtered, nil
}

// RuleCacheOptions configures the in-memory host alert rule snapshot.  Rules
// are loaded once at Start and then only by snapshotcache's ticker.  CRUD
// handlers must not call Refresh or otherwise invalidate this cache.
type RuleCacheOptions struct {
	Repository         HostRuleSource
	RefreshInterval    time.Duration
	RefreshTimeout     time.Duration
	InitialLoadTimeout time.Duration
}

// RuleCache keeps enabled mooxsys host rules out of the HostMetric hot
// path.  Each published snapshot is immutable from the consumer's point of
// view; callers receive values or freshly allocated slices.
type RuleCache struct {
	cache *snapshotcache.Cache[domain.AlertRule]
}

func NewRuleCache(opts RuleCacheOptions) (*RuleCache, error) {
	if opts.Repository == nil {
		return nil, errors.New("host alert rule repository is required")
	}
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = defaultHostRuleRefreshPeriod
	}
	cache, err := snapshotcache.New(snapshotcache.Options[domain.AlertRule]{
		Name:               "monitor-host-alert-rules",
		Source:             hostRuleSource{repository: opts.Repository},
		Indexes:            []snapshotcache.Index[domain.AlertRule]{{Name: hostRuleIndex, Key: func(rule domain.AlertRule) []string { return []string{rule.CheckID} }}, {Name: hostRuleIDIndex, Unique: true, Key: func(rule domain.AlertRule) []string { return []string{rule.RuleID} }}},
		RefreshInterval:    opts.RefreshInterval,
		RefreshTimeout:     opts.RefreshTimeout,
		InitialLoadTimeout: opts.InitialLoadTimeout,
		Startup:            snapshotcache.StartupOptions{FailIfNoSnapshot: false},
	})
	if err != nil {
		return nil, fmt.Errorf("create host alert rule cache: %w", err)
	}
	return &RuleCache{cache: cache}, nil
}

func (c *RuleCache) Start(ctx context.Context) error {
	if c == nil || c.cache == nil {
		return errors.New("host alert rule cache is nil")
	}
	return c.cache.Start(ctx)
}

func (c *RuleCache) Stop(ctx context.Context) error {
	if c == nil || c.cache == nil {
		return nil
	}
	return c.cache.Stop(ctx)
}

// Rules returns all enabled rules for one agent and metric.  It performs only
// an in-memory indexed lookup; no repository or SQLite call is made here.
func (c *RuleCache) Rules(agentID, metric string) []domain.AlertRule {
	if c == nil || c.cache == nil || strings.TrimSpace(agentID) == "" || !isHostMetric(metric) {
		return nil
	}
	items := c.cache.List(snapshotcache.Query[domain.AlertRule]{
		Filters: []snapshotcache.Filter[domain.AlertRule]{snapshotcache.Eq[domain.AlertRule](hostRuleIndex, HostRuleKey(agentID, metric))},
	})
	out := make([]domain.AlertRule, len(items))
	for i, item := range items {
		out[i] = cloneHostRule(item)
	}
	return out
}

// Rule returns the first rule for an agent and metric.  Rules is preferred
// when multiple thresholds are configured for one metric.
func (c *RuleCache) Rule(agentID, metric string) (domain.AlertRule, bool) {
	rules := c.Rules(agentID, metric)
	if len(rules) == 0 {
		return domain.AlertRule{}, false
	}
	return rules[0], true
}

// Snapshot returns a copied immutable view for diagnostics and tests.
func (c *RuleCache) Snapshot() []domain.AlertRule {
	if c == nil || c.cache == nil {
		return nil
	}
	snapshot := c.cache.Snapshot()
	if snapshot == nil {
		return nil
	}
	out := make([]domain.AlertRule, len(snapshot.Items))
	for i, item := range snapshot.Items {
		out[i] = cloneHostRule(item)
	}
	return out
}

func (c *RuleCache) Status() snapshotcache.Status {
	if c == nil || c.cache == nil {
		return snapshotcache.Status{}
	}
	return c.cache.Status()
}

func cloneHostRule(rule domain.AlertRule) domain.AlertRule {
	return rule
}
