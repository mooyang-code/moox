package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerttext"
	"github.com/mooyang-code/moox/modules/monitor/internal/checks"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
)

// buildMonitorMarketCanary 返回探针域每轮的评估：行情探针每个配置项一个事实，内置标签每个一个事实。
// Collector 暂停或恢复追赶时整域跳过（保持原状态）；Storage 标签列表读不到时本轮报错、不推进；
// Collector 的标签同步状态读不到时标签检查整类冻结。探针关闭时返回 nil，由 retireMarketCanaries 在启动时把旧检查删掉。
func buildMonitorMarketCanary(
	cfg *config.Config,
	runtime *Runtime,
	applier *checks.Applier,
	metricsStorage *monmetrics.StorageAdapter,
	tagRuns checks.TagSyncStatusSource,
	halted func(context.Context) bool,
) (func(context.Context) error, error) {
	if cfg == nil || !cfg.MarketCanary.Enabled {
		return nil, nil
	}
	if runtime == nil || applier == nil {
		return nil, fmt.Errorf("monitor market canary requires repositories")
	}
	if runtime.StorageGateway == nil {
		return nil, fmt.Errorf("monitor market canary requires the shared Storage gateway client")
	}
	reader := runtime.StorageGateway
	type canaryCheck struct {
		canary watchdog.MarketCanary
		name   string
	}
	canaries := make([]canaryCheck, 0, len(cfg.MarketCanary.Subjects))
	for _, subject := range cfg.MarketCanary.Subjects {
		canaryConfig := watchdog.MarketCanaryConfig{
			SpaceID: subject.SpaceID, DatasetID: subject.DatasetID, SubjectID: subject.Symbol, Frequency: subject.Frequency,
			SeriesTag: subject.SeriesTag,
			Freshness: cfg.MarketCanary.Freshness, ReturnThreshold: cfg.MarketCanary.ReturnThreshold, MarketID: subject.MarketID,
			SettleDelay: cfg.MarketCanary.SettleDelay, PostCloseDelay: cfg.MarketCanary.PostCloseDelay, CalendarWarningLead: cfg.MarketCanary.CalendarWarningLead,
			ClosedBarCount: cfg.MarketCanary.ClosedBarCount, ClosedBarMinCoverage: cfg.MarketCanary.ClosedBarMinCoverage,
			EligibleKlineProviders: append([]string(nil), subject.EligibleKlineProviders...),
		}
		canaries = append(canaries, canaryCheck{
			canary: watchdog.MarketCanary{Reader: reader, Config: canaryConfig},
			name:   "行情探针 · " + canaryConfig.SubjectID + " " + alerttext.Frequency(canaryConfig.Frequency) + "K线",
		})
	}
	return func(runCtx context.Context) error {
		// Collector 模块暂停或恢复追赶时采集是有意停下的：本轮不跑探针，检查保持原状态。
		if halted != nil && halted(runCtx) {
			return nil
		}
		round := checks.Round{Domain: checks.DomainCanary}
		for _, item := range canaries {
			result := item.canary.Run(runCtx)
			round.Facts = append(round.Facts, checks.Fact{
				SpaceID: result.SpaceID, CheckID: result.CheckID, Name: item.name,
				Status: checks.PassStatus(result.Success), Reason: result.Reason, Diagnostic: result.Diagnostic,
			})
		}
		if metricsStorage != nil && tagRuns != nil {
			tagFacts, available, err := checks.SubjectTagFacts(runCtx, metricsStorage, tagRuns, time.Now().UTC())
			if err != nil {
				return err
			}
			if !available {
				round.FrozenPrefixes = append(round.FrozenPrefixes, checks.SubjectTagPrefix)
			}
			round.Facts = append(round.Facts, tagFacts...)
		} else {
			round.FrozenPrefixes = append(round.FrozenPrefixes, checks.SubjectTagPrefix)
		}
		return applier.Apply(runCtx, round)
	}, nil
}

// retireMarketCanaries 在行情探针关闭时把探针域的旧检查全部删掉（在告警的推一条恢复）：关闭后不再有采样能让它们恢复。
func retireMarketCanaries(ctx context.Context, cfg *config.Config, applier *checks.Applier) error {
	if applier == nil || (cfg != nil && cfg.MarketCanary.Enabled) {
		return nil
	}
	return applier.Apply(ctx, checks.Round{Domain: checks.DomainCanary})
}
