package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
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
) (func(context.Context) error, error) {
	if cfg == nil || !cfg.MarketCanary.Enabled {
		return nil, nil
	}
	if runtime == nil || runtime.Repositories == nil {
		return nil, fmt.Errorf("monitor market canary requires repositories")
	}
	credentials, err := gatewayauth.ResolveCredentials(cfg.Metrics.Storage.KeyID, cfg.Metrics.Storage.HMACKeyFile)
	if err != nil {
		return nil, fmt.Errorf("monitor market canary credentials: %w", err)
	}
	reader := storagepb.NewPrimaryStoreClientProxy(gatewayauth.NewTRPCClientOptions(
		gatewayauth.ServiceGatewayTarget(cfg.Metrics.Storage.GatewayTarget),
		firstNonEmptyString(cfg.Metrics.Storage.GatewayNodeID, gatewayauth.ServiceGatewayNodeID()),
		credentials,
	)...)
	canaries := make([]watchdog.MarketCanary, 0, len(cfg.MarketCanary.Subjects))
	for _, subject := range cfg.MarketCanary.Subjects {
		canaryConfig := watchdog.MarketCanaryConfig{
			SpaceID: subject.SpaceID, DatasetID: subject.DatasetID, SubjectID: subject.Symbol, Frequency: subject.Frequency,
			Freshness: cfg.MarketCanary.Freshness, ReturnThreshold: cfg.MarketCanary.ReturnThreshold,
			VolumeRatioThreshold: cfg.MarketCanary.VolumeRatioThreshold,
		}
		check := domain.Check{
			SpaceID: canaryConfig.SpaceID, CheckID: watchdog.MarketCanaryCheckID(canaryConfig),
			Name:      "Market canary " + strings.Join([]string{canaryConfig.DatasetID, canaryConfig.SubjectID, canaryConfig.Frequency}, "/"),
			GroupName: "business", Kind: domain.CheckKindExternal, Source: domain.CheckSourceManual,
			Enabled: true, IntervalSeconds: 30, TimeoutMS: 20000,
		}
		existing, getErr := runtime.Repositories.Checks.Get(ctx, check.SpaceID, check.CheckID)
		switch {
		case getErr == nil:
			check.ID = existing.ID
			if err := runtime.Repositories.Checks.Update(ctx, &check); err != nil {
				return nil, err
			}
		case errors.Is(getErr, gorm.ErrRecordNotFound):
			if err := runtime.Repositories.Checks.Create(ctx, &check); err != nil {
				return nil, err
			}
		default:
			return nil, getErr
		}
		canaries = append(canaries, watchdog.MarketCanary{Reader: reader, Config: canaryConfig})
	}
	return func(runCtx context.Context) error {
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
	}, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
