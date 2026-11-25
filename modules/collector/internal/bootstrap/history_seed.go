package bootstrap

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
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

// historySeedOptions 按配置装配历史补齐：深度取 min(保留期 − 1 天, 行情源允许的最长历史, max_depth)；
// 分块并发是市场补采函数的“地域数 × 预留并发”，没有云函数地域的市场（crypto 默认）走本地执行端，用空间的补采并发上限。
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
		if limit := seed.depthCap(spaceID); limit > 0 {
			depth = min(depth, limit)
		}
		return depth, nil
	}
	options.Slots = func(ctx context.Context, spaceID string) int {
		reconciler := reconcilers.Get(spaceID)
		if reconciler == nil {
			return 0
		}
		slots := 0
		for market := range cfg.SCFMarkets[strings.ToLower(spaceID)] {
			slots += len(reconciler.BackfillRegions(market)) * reconciler.BackfillReservedConcurrency()
		}
		return slots
	}
	return options
}
