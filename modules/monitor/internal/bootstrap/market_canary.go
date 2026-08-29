package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/storageauth"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"gorm.io/gorm"
)

func buildMonitorMarketCanary(
	ctx context.Context,
	cfg *config.Config,
	runtime *Runtime,
	hook func(context.Context, domain.Check, domain.CheckResult),
) (func(context.Context) error, func(context.Context) error, error) {
	if cfg == nil || !cfg.MarketCanary.Enabled {
		return nil, nil, nil
	}
	if runtime == nil || runtime.Repositories == nil {
		return nil, nil, fmt.Errorf("monitor market canary requires repositories")
	}
	credentials, err := gatewayauth.ResolveCredentials(cfg.Metrics.Storage.KeyID, cfg.Metrics.Storage.HMACKeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("monitor market canary credentials: %w", err)
	}
	reader := storagepb.NewPrimaryStoreClientProxy(gatewayauth.NewTRPCClientOptions(
		cfg.Metrics.Storage.GatewayTarget,
		firstNonEmptyString(cfg.Metrics.Storage.GatewayNodeID, gatewayauth.ServiceGatewayNodeID()),
		credentials,
	)...)
	canaries := make([]watchdog.MarketCanary, 0, len(cfg.MarketCanary.Subjects))
	configuredCheckIDs := make(map[string]struct{}, len(cfg.MarketCanary.Subjects))
	for _, subject := range cfg.MarketCanary.Subjects {
		canaryConfig := watchdog.MarketCanaryConfig{
			SpaceID: subject.SpaceID, DatasetID: subject.DatasetID, SubjectID: subject.Symbol, Frequency: subject.Frequency,
			SeriesTag: subject.SeriesTag,
			Freshness: cfg.MarketCanary.Freshness, ReturnThreshold: cfg.MarketCanary.ReturnThreshold,
			MarketID: subject.MarketID, CalendarPath: subject.CalendarPath,
			SettleDelay: cfg.MarketCanary.SettleDelay, CalendarWarningLead: cfg.MarketCanary.CalendarWarningLead,
			ClosedBarCount: cfg.MarketCanary.ClosedBarCount, EligibleKlineProviders: append([]string(nil), subject.EligibleKlineProviders...),
		}
		check := domain.Check{
			SpaceID: canaryConfig.SpaceID, CheckID: watchdog.MarketCanaryCheckID(canaryConfig),
			Name:      "Market canary " + watchdog.MarketCanaryTarget(canaryConfig),
			GroupName: "business", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability,
			Enabled: true, IntervalSeconds: 30, TimeoutMS: 20000,
		}
		configuredCheckIDs[check.CheckID] = struct{}{}
		existing, getErr := runtime.Repositories.Checks.Get(ctx, check.SpaceID, check.CheckID)
		switch {
		case getErr == nil:
			check.ID = existing.ID
			if err := runtime.Repositories.Checks.Update(ctx, &check); err != nil {
				return nil, nil, err
			}
		case errors.Is(getErr, gorm.ErrRecordNotFound):
			if err := runtime.Repositories.Checks.Create(ctx, &check); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, getErr
		}
		canaries = append(canaries, watchdog.MarketCanary{
			Reader: reader, AuthInfo: storageauth.Primary("monitor-market-canary"), Config: canaryConfig,
		})
	}
	// Canary identity includes the symbol. When configuration moves from an
	// inactive symbol to an active one, leave no enabled check behind for the
	// old identity; otherwise its last failed result remains visible forever in
	// the business overview and can continue to drive a stale alert.
	checks, err := runtime.Repositories.Checks.List(ctx, store.ListChecksOptions{
		Source: domain.CheckSourceObservability,
		Page:   store.Page{PageSize: 500},
	})
	if err != nil {
		return nil, nil, err
	}
	for index := range checks {
		check := &checks[index]
		if !strings.HasPrefix(check.CheckID, "market_canary:") {
			continue
		}
		if _, keep := configuredCheckIDs[check.CheckID]; keep {
			continue
		}
		rules, err := runtime.Repositories.Alerts.ListRulesForCheck(ctx, check.SpaceID, check.CheckID)
		if err != nil {
			return nil, nil, err
		}
		// Remove obsolete rules instead of merely disabling them. DeleteRule also
		// removes a previously firing AlertState, so a retired symbol cannot keep
		// contributing a stale alert to the availability overview.
		for index := range rules {
			if err := runtime.Repositories.Alerts.DeleteRule(ctx, rules[index].SpaceID, rules[index].RuleID); err != nil {
				return nil, nil, err
			}
		}
		check.Enabled = false
		if err := runtime.Repositories.Checks.Update(ctx, check); err != nil {
			return nil, nil, err
		}
	}
	run := func(runCtx context.Context) error {
		var errs []error
		for _, canary := range canaries {
			result := canary.Run(runCtx)
			inserted, err := runtime.Repositories.Results.InsertIfAbsent(runCtx, &result)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !inserted {
				continue
			}
			check, err := runtime.Repositories.Checks.Get(runCtx, result.SpaceID, result.CheckID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			check.LastCheckedAt = &result.CheckedAt
			if err := runtime.Repositories.Checks.Update(runCtx, check); err != nil {
				errs = append(errs, err)
				continue
			}
			if hook != nil {
				hook(runCtx, *check, result)
			}
		}
		return errors.Join(errs...)
	}
	probe := func(probeCtx context.Context) error {
		for _, canary := range canaries {
			if err := canary.ProbeStorageAuth(probeCtx); err != nil {
				return fmt.Errorf("%s: %w", watchdog.MarketCanaryTarget(canary.Config), err)
			}
		}
		return nil
	}
	return run, probe, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
