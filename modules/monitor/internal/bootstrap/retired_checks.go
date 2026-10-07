package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

// retireObsoleteBusinessChecks disables K-line checks when K-line freshness is
// turned off and removes alert rules left on disabled checks.
func retireObsoleteBusinessChecks(ctx context.Context, repositories *store.Repositories, cfg *config.Config) error {
	if repositories == nil {
		return fmt.Errorf("retire obsolete checks requires repositories")
	}
	checks, err := listAllMonitorChecks(ctx, repositories)
	if err != nil {
		return err
	}
	for index := range checks {
		check := &checks[index]
		retire := false
		if strings.HasPrefix(check.CheckID, "kline_freshness:") {
			// K-line checks are task-owned and discovered dynamically. Keep prior
			// identities until a successful inventory evaluation can resolve them.
			retire = cfg == nil || !cfg.KlineFreshness.Enabled
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
