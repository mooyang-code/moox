package bootstrap

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
)

// seedRetentionMargin 是补齐深度比保留期少留的余量：免得补进去的最早那天马上被保留期清理删掉。
const seedRetentionMargin = 24 * time.Hour

// seedDefaultRowsPerSecond、seedDefaultLocalRowsPerSecond 是补齐写 Storage 的速率上限默认值。
const (
	seedDefaultRowsPerSecond      = 10000
	seedDefaultLocalRowsPerSecond = 1000
)

// datasetReader 读数据集的元数据（保留期）。
type datasetReader interface {
	GetDataset(ctx context.Context, spaceID, datasetID string) (storagesource.DatasetInfo, error)
}

// historySeedOptions 按配置装配历史补齐：深度取 min(保留期 − 1 天, max_depth)，行情源的回看上限按行情源目录的声明
// （Lookback，补齐另减 1 天余量）；
// 分块并发是各市场补采函数里最大的“地域数 × 预留并发”，没有云函数地域的市场（crypto 默认）走本地执行端，用空间的补采并发上限。
func historySeedOptions(cfg *Config, datasets datasetReader, reconcilers *fetchplan.Reconcilers) klinecheck.SeedOptions {
	seed := cfg.HistorySeed
	options := klinecheck.SeedOptions{
		Enabled:            seed.enabled(),
		SubjectsPerJob:     seed.SubjectsPerJob,
		MergeSessions:      seed.mergeSessions(),
		RowsPerSecond:      seed.MaxRowsPerSecond,
		LocalRowsPerSecond: seed.LocalMaxRowsPerSecond,
	}
	if options.RowsPerSecond <= 0 {
		options.RowsPerSecond = seedDefaultRowsPerSecond
	}
	if options.LocalRowsPerSecond <= 0 {
		options.LocalRowsPerSecond = seedDefaultLocalRowsPerSecond
	}
	options.Depth = func(ctx context.Context, spaceID, datasetID, frequency string) (time.Duration, error) {
		depth := seed.maxDepth()
		info, err := datasets.GetDataset(ctx, spaceID, datasetID)
		if err != nil {
			return 0, err
		}
		if window, ok := info.RetentionWindow(); ok {
			depth = min(depth, window-seedRetentionMargin)
		}
		return depth, nil
	}
	options.Lookback = marketwiring.KlineLookback
	// 只用写入直达存储机的地域：入口路由的主机就是 scf_entry 的主机（存储机）。crypto 的地域常配在香港、入口是海外机，
	// 补齐几千万行经海外机转发会挤掉实时采集，这样的地域不算，crypto 补齐走本地执行端并受更严的限速。
	options.Regions = func(spaceID, marketType string) []string {
		reconciler := reconcilers.Get(spaceID)
		if reconciler == nil {
			return nil
		}
		var direct []string
		for _, region := range reconciler.BackfillRegions(marketType) {
			if route, err := cfg.scfPlanEntryEnvironmentRoute(region); err == nil && route.Host == cfg.SCFEntry.Host {
				direct = append(direct, region)
			}
		}
		return direct
	}
	options.Slots = func(ctx context.Context, spaceID string) int {
		reconciler := reconcilers.Get(spaceID)
		if reconciler == nil {
			return 0
		}
		// 取各市场里最大的一个：同一时刻的分块多半来自同一个数据集（同一个市场），名额超过它的补采函数并发上限，
		// 多出的调用会被平台拒绝或排队，占着名额却没有进展。
		slots := 0
		for market := range cfg.SCFMarkets[strings.ToLower(spaceID)] {
			slots = max(slots, len(options.Regions(spaceID, market))*reconciler.BackfillReservedConcurrency())
		}
		return slots
	}
	return options
}
