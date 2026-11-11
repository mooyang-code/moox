package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// targetHistoryRetention 是目标回执、影子目标在有效期截止之后保留的时长。
	targetHistoryRetention = 7 * 24 * time.Hour
	// targetHistoryBatch 是每批删除的行数。
	targetHistoryBatch = 5000
	// targetHistoryBatchPause 是批与批之间的停顿，把唯一的数据库连接让给交易读写。
	targetHistoryBatchPause = 50 * time.Millisecond
)

// TargetHistoryPruner 删掉有效期截止已超过 7 天的目标回执和影子目标。它们每分钟一份、每份几十 KB，不清理会一直涨；
// 读它们的只有接收时的幂等检查（只在目标有效期内起作用）、换手告警（最近 1 小时外加 1 条基准）和影子目标查看（最近 100 条），
// 7 天前的行已经用不上。按有效期截止判断，保证删掉的都已过期。实现 FundMaintainer，由 FundWorker 定时运行；
// 清理不是交易行为，不受暂停影响。
type TargetHistoryPruner struct {
	Store *store.Store
	Now   func() time.Time

	// batch 是每批删除的行数，为 0 时取 targetHistoryBatch；只有测试会改。
	batch int
}

// RunAll 分批删完两张表里的过期行。
func (p *TargetHistoryPruner) RunAll(ctx context.Context) error {
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	before := now.Add(-targetHistoryRetention).UnixMilli()
	batch := p.batch
	if batch <= 0 {
		batch = targetHistoryBatch
	}
	receipts, receiptErr := deleteInBatches(ctx, batch, func(limit int) (int64, error) {
		return p.Store.DeleteTargetReceiptsBefore(ctx, before, limit)
	})
	shadows, shadowErr := deleteInBatches(ctx, batch, func(limit int) (int64, error) {
		return p.Store.DeleteShadowTargetsBefore(ctx, before, limit)
	})
	if receipts > 0 || shadows > 0 {
		log.Infof("交易：清理有效期截止超过 7 天的目标回执 %d 条、影子目标 %d 条", receipts, shadows)
	}
	return errors.Join(receiptErr, shadowErr)
}

// deleteInBatches 反复调用 deleteBatch，直到某一批删不满 batch 行；批与批之间停一下，让出连接。
func deleteInBatches(ctx context.Context, batch int, deleteBatch func(limit int) (int64, error)) (int64, error) {
	var total int64
	for {
		deleted, err := deleteBatch(batch)
		total += deleted
		if err != nil || deleted < int64(batch) {
			return total, err
		}
		timer := time.NewTimer(targetHistoryBatchPause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return total, ctx.Err()
		case <-timer.C:
		}
	}
}
