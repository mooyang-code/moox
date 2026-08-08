// Package ruleseed loads and safely applies the built-in Collector rule bundle.
package ruleseed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

type ruleSeed struct {
	Rules []ruleSeedItem `yaml:"rules"`
}

type ruleSeedItem struct {
	SpaceID       string         `yaml:"space_id"`
	RuleID        string         `yaml:"rule_id"`
	DataType      string         `yaml:"data_type"`
	Provider      string         `yaml:"provider"`
	MarketType    string         `yaml:"market_type"`
	Enabled       bool           `yaml:"enabled"`
	Creator       string         `yaml:"creator"`
	CollectParams map[string]any `yaml:"collect_params"`
}

// SeedSummary reports missing-only rule application results.
type SeedSummary struct {
	Created   int
	Unchanged int
}

// Load reads and validates a Collector rule bundle.
func Load(r io.Reader) ([]domain.TaskRule, error) {
	return loadRuleSeed(r)
}

// LoadFile reads and validates a Collector rule bundle from disk.
func LoadFile(path string) ([]domain.TaskRule, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("seed file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open seed file: %w", err)
	}
	defer file.Close()
	return Load(file)
}

func loadRuleSeed(r io.Reader) ([]domain.TaskRule, error) {
	if r == nil {
		return nil, fmt.Errorf("seed reader is required")
	}
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	var seed ruleSeed
	if err := decoder.Decode(&seed); err != nil {
		return nil, fmt.Errorf("decode rule seed: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode rule seed: multiple YAML documents are not supported")
		}
		return nil, fmt.Errorf("decode rule seed: %w", err)
	}
	if len(seed.Rules) == 0 {
		return nil, fmt.Errorf("rule seed must contain at least one rule")
	}

	rules := make([]domain.TaskRule, 0, len(seed.Rules))
	seen := make(map[string]struct{}, len(seed.Rules))
	for index, item := range seed.Rules {
		rule, err := validateRuleSeedItem(item)
		if err != nil {
			return nil, fmt.Errorf("rules[%d]: %w", index, err)
		}
		key := rule.SpaceID + "\x00" + rule.RuleID
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("rules[%d]: duplicate rule %s/%s", index, rule.SpaceID, rule.RuleID)
		}
		seen[key] = struct{}{}
		rules = append(rules, rule)
	}
	return rules, nil
}

func validateRuleSeedItem(item ruleSeedItem) (domain.TaskRule, error) {
	spaceID := strings.TrimSpace(item.SpaceID)
	ruleID := strings.TrimSpace(item.RuleID)
	dataType := strings.ToLower(strings.TrimSpace(item.DataType))
	provider := strings.ToLower(strings.TrimSpace(item.Provider))
	marketType := strings.ToLower(strings.TrimSpace(item.MarketType))
	if spaceID == "" || ruleID == "" || dataType == "" || provider == "" || marketType == "" {
		return domain.TaskRule{}, fmt.Errorf("space_id, rule_id, data_type, provider and market_type are required")
	}
	if item.CollectParams == nil {
		return domain.TaskRule{}, fmt.Errorf("collect_params is required")
	}
	rawParams, err := json.Marshal(item.CollectParams)
	if err != nil {
		return domain.TaskRule{}, fmt.Errorf("marshal collect_params: %w", err)
	}
	params, err := domain.ParseCollectParams(string(rawParams), provider, marketType, dataType)
	if err != nil {
		return domain.TaskRule{}, fmt.Errorf("collect_params: %w", err)
	}
	if err := params.Validate(); err != nil {
		return domain.TaskRule{}, fmt.Errorf("collect_params: %w", err)
	}
	if params.Provider != provider || params.MarketType != marketType || params.Collector.DataType != dataType {
		return domain.TaskRule{}, fmt.Errorf("collect_params provider, market_type and data_type must match rule fields")
	}
	canonical, err := json.Marshal(params)
	if err != nil {
		return domain.TaskRule{}, fmt.Errorf("marshal canonical collect_params: %w", err)
	}
	return domain.TaskRule{
		SpaceID:       spaceID,
		RuleID:        ruleID,
		DataType:      dataType,
		Provider:      provider,
		MarketType:    marketType,
		CollectParams: string(canonical),
		Enabled:       item.Enabled,
		Creator:       strings.TrimSpace(item.Creator),
	}, nil
}

// SeedMissing inserts only absent (space_id, rule_id) pairs. Existing rows are
// deliberately left untouched so a user disable or edit survives redeploy.
func SeedMissing(ctx context.Context, repo *store.TaskRuleRepository, rules []domain.TaskRule) (SeedSummary, error) {
	if repo == nil {
		return SeedSummary{}, fmt.Errorf("task rule repository is required")
	}
	var summary SeedSummary
	for _, rule := range rules {
		_, err := repo.GetByRuleID(ctx, rule.SpaceID, rule.RuleID)
		switch {
		case err == nil:
			summary.Unchanged++
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := repo.Create(ctx, rule); err != nil {
				return summary, fmt.Errorf("create rule %s/%s: %w", rule.SpaceID, rule.RuleID, err)
			}
			summary.Created++
		default:
			return summary, fmt.Errorf("check rule %s/%s: %w", rule.SpaceID, rule.RuleID, err)
		}
	}
	return summary, nil
}
