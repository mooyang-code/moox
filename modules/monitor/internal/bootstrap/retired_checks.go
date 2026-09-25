package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

func retiredDatasetCheckID(checkID string) bool {
	checkID = strings.TrimSpace(checkID)
	if !strings.HasPrefix(checkID, "dataset:") {
		return false
	}
	if strings.HasSuffix(checkID, ":1H") {
		return true
	}
	return strings.Contains(checkID, ":dataset_collector_") ||
		strings.Contains(checkID, ":view_collector_") ||
		strings.Contains(checkID, ":view_crypto_") ||
		strings.Contains(checkID, ":dataset_perpetual_") ||
		strings.Contains(checkID, ":dataset_spot_kline_1h")
}

func retireObsoleteBusinessChecks(ctx context.Context, repositories *store.Repositories, cfg *config.Config) error {
	if repositories == nil {
		return fmt.Errorf("retire obsolete checks requires repositories")
	}
	checks, err := listAllMonitorChecks(ctx, repositories)
	if err != nil {
		return err
	}
	configuredKline := configuredKlineFreshnessCheckIDs(cfg)
	for index := range checks {
		check := &checks[index]
		retire := retiredDatasetCheckID(check.CheckID)
		if strings.HasPrefix(check.CheckID, "kline_freshness:") {
			_, keep := configuredKline[check.CheckID]
			retire = !keep
		}
		if !retire {
			continue
		}
		if err := disableCheckAndDeleteRules(ctx, repositories, check); err != nil {
			return err
		}
	}
	return deleteAlertRulesForDisabledChecks(ctx, repositories, checks)
}

func configuredKlineFreshnessCheckIDs(cfg *config.Config) map[string]struct{} {
	ids := map[string]struct{}{}
	if cfg == nil || !cfg.KlineFreshness.Enabled {
		return ids
	}
	for _, rule := range cfg.KlineFreshness.Rules {
		if !rule.Enabled {
			continue
		}
		ids[monmetrics.KlineFreshnessCheckID(monmetrics.KlineFreshnessRule{
			SpaceID: rule.SpaceID, ViewID: rule.ViewID, Frequency: rule.Frequency,
		})] = struct{}{}
	}
	return ids
}

func disableCheckAndDeleteRules(ctx context.Context, repositories *store.Repositories, check *domain.Check) error {
	if repositories == nil || check == nil {
		return fmt.Errorf("disable check requires repositories and check")
	}
	rules, err := repositories.Alerts.ListRulesForCheck(ctx, check.SpaceID, check.CheckID)
	if err != nil {
		return err
	}
	for index := range rules {
		if err := repositories.Alerts.DeleteRule(ctx, rules[index].SpaceID, rules[index].RuleID); err != nil {
			return err
		}
	}
	if !check.Enabled {
		return nil
	}
	check.Enabled = false
	return repositories.Checks.Update(ctx, check)
}

func deleteAlertRulesForDisabledChecks(ctx context.Context, repositories *store.Repositories, checks []domain.Check) error {
	if repositories == nil {
		return fmt.Errorf("delete disabled-check rules requires repositories")
	}
	for index := range checks {
		check := &checks[index]
		if check.Enabled {
			continue
		}
		rules, err := repositories.Alerts.ListRulesForCheck(ctx, check.SpaceID, check.CheckID)
		if err != nil {
			return err
		}
		for ruleIndex := range rules {
			if err := repositories.Alerts.DeleteRule(ctx, rules[ruleIndex].SpaceID, rules[ruleIndex].RuleID); err != nil {
				return err
			}
		}
	}
	return nil
}
