package runtime

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const defaultEquityDownsampleBatch = 2000

// EquityHistoryDownsampler 按分层保留策略对分钟净值历史（执行账户与组合账户两张表）执行分批降采样，
// 防止单表随运行时间无限膨胀（见 store.DefaultEquityRetentionTiers）。
// 实现 FundMaintainer 接口，由 FundWorker 定期驱动。
type EquityHistoryDownsampler struct {
	Store      *store.Store
	Now        func() time.Time
	BatchLimit int
	Tiers      []store.EquityRetentionTier
}

func (d *EquityHistoryDownsampler) RunAll(ctx context.Context) error {
	if d == nil || d.Store == nil {
		return nil
	}
	now := time.Now().UTC()
	if d.Now != nil {
		now = d.Now().UTC()
	}
	batch := d.BatchLimit
	if batch <= 0 {
		batch = defaultEquityDownsampleBatch
	}
	deleted, err := d.Store.DownsampleEquityPoints(ctx, now, batch, d.Tiers...)
	if err != nil {
		return err
	}
	if deleted > 0 {
		log.InfoContextf(ctx, "分钟净值历史降采样完成：清理冗余点 %d 条", deleted)
	}
	return nil
}
