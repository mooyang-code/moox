package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/storageauth"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/robfig/cron"
	"gorm.io/gorm"
)

const (
	tagStaleFactor   = 3
	tagCheckPrefix   = "subject_tag:"
	sqliteTimeLayout = "2006-01-02 15:04:05"
)

// checkTag evaluates the health of one tag definition. Member counts are
// queried by Storage, while freshness is derived from the tag's own cron and
// timezone so a slow or failed sync cannot be mistaken for a healthy catalog.
func checkTag(tag *storagepb.Tag, now time.Time) []string {
	if tag == nil {
		return []string{"missing"}
	}
	reasons := make([]string, 0, 3)
	if tag.GetLastStatus() == "failed" {
		reasons = append(reasons, "failed")
	}
	if tag.GetActiveCount() == 0 {
		reasons = append(reasons, "no_active_members")
	}
	if tagStale(tag, now) {
		reasons = append(reasons, "stale")
	}
	if len(reasons) == 0 {
		return nil
	}
	return reasons
}

func tagStale(tag *storagepb.Tag, now time.Time) bool {
	lastRun, err := time.ParseInLocation(sqliteTimeLayout, tag.GetLastRunAt(), time.UTC)
	if err != nil {
		return true
	}
	schedule, err := cron.ParseStandard(tag.GetCron())
	if err != nil {
		return true
	}
	loc, err := time.LoadLocation(firstNonEmptyString(tag.GetTimezone(), "UTC"))
	if err != nil {
		return true
	}
	next := schedule.Next(lastRun.In(loc))
	period := schedule.Next(next).Sub(next)
	return now.Sub(lastRun) > time.Duration(tagStaleFactor)*period
}

func subjectTagCheckID(spaceID, tagID string) string {
	return tagCheckPrefix + strings.TrimSpace(spaceID) + ":" + strings.TrimSpace(tagID)
}

func buildMonitorMarketCanary(
	ctx context.Context,
	cfg *config.Config,
	runtime *Runtime,
	metricsStorage *monmetrics.StorageAdapter,
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
	tagSpaces := make([]string, 0, len(cfg.MarketCanary.Subjects))
	seenTagSpaces := make(map[string]struct{})
	configuredCheckIDs := make(map[string]struct{}, len(cfg.MarketCanary.Subjects))
	for _, subject := range cfg.MarketCanary.Subjects {
		if spaceID := strings.TrimSpace(subject.SpaceID); spaceID != "" {
			if _, ok := seenTagSpaces[spaceID]; !ok {
				seenTagSpaces[spaceID] = struct{}{}
				tagSpaces = append(tagSpaces, spaceID)
			}
		}
		canaryConfig := watchdog.MarketCanaryConfig{
			SpaceID: subject.SpaceID, DatasetID: subject.DatasetID, SubjectID: subject.Symbol, Frequency: subject.Frequency,
			SeriesTag: subject.SeriesTag,
			Freshness: cfg.MarketCanary.Freshness, ReturnThreshold: cfg.MarketCanary.ReturnThreshold,
			MarketID: subject.MarketID, CalendarPath: subject.CalendarPath,
			SettleDelay: cfg.MarketCanary.SettleDelay, PostCloseDelay: cfg.MarketCanary.PostCloseDelay, CalendarWarningLead: cfg.MarketCanary.CalendarWarningLead,
			ClosedBarCount: cfg.MarketCanary.ClosedBarCount, ClosedBarMinCoverage: cfg.MarketCanary.ClosedBarMinCoverage,
			EligibleKlineProviders: append([]string(nil), subject.EligibleKlineProviders...),
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
	if metricsStorage != nil {
		previousRun := run
		run = func(runCtx context.Context) error {
			err := previousRun(runCtx)
			now := time.Now().UTC()
			for _, spaceID := range tagSpaces {
				tags, tagErr := metricsStorage.ListTags(runCtx, spaceID)
				if tagErr != nil {
					err = errors.Join(err, tagErr)
					continue
				}
				for _, tag := range tags {
					if tag == nil || !tag.GetBuiltin() {
						continue
					}
					checkID := subjectTagCheckID(spaceID, tag.GetTagId())
					check, getErr := runtime.Repositories.Checks.Get(runCtx, spaceID, checkID)
					if errors.Is(getErr, gorm.ErrRecordNotFound) {
						check = &domain.Check{SpaceID: spaceID, CheckID: checkID, Name: "Subject tag " + tag.GetTagName(), GroupName: "business", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true, IntervalSeconds: 30, TimeoutMS: 20000}
						getErr = runtime.Repositories.Checks.Create(runCtx, check)
					} else if getErr == nil && check.Name != "Subject tag "+tag.GetTagName() {
						check.Name = "Subject tag " + tag.GetTagName()
						getErr = runtime.Repositories.Checks.Update(runCtx, check)
					}
					if getErr != nil {
						err = errors.Join(err, getErr)
						continue
					}
					reasons := checkTag(tag, now)
					result := domain.CheckResult{ResultID: fmt.Sprintf("%s-%d", checkID, now.UnixNano()), SpaceID: spaceID, CheckID: checkID, InstanceID: "monitor", Success: len(reasons) == 0, Connected: true, Status: domain.CheckStatusOK, ErrorMessage: strings.Join(reasons, ","), CheckedAt: now, CreatedAt: now}
					if !result.Success {
						result.Status = domain.CheckStatusDown
					}
					inserted, insertErr := runtime.Repositories.Results.InsertIfAbsent(runCtx, &result)
					if insertErr != nil {
						err = errors.Join(err, insertErr)
						continue
					}
					if inserted {
						check.LastCheckedAt = &now
						if updateErr := runtime.Repositories.Checks.Update(runCtx, check); updateErr != nil {
							err = errors.Join(err, updateErr)
						} else if hook != nil {
							hook(runCtx, *check, result)
						}
					}
				}
			}
			return err
		}
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
