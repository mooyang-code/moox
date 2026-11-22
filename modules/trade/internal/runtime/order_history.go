package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// orderHistoryRetention 是已结束订单与成交在结束之后保留的时长。
	orderHistoryRetention = 5 * 24 * time.Hour
	// orderHistoryBatch 是每批删除的订单数（连同它们的成交）。本地 30 万单的库上每批 500 单约 76ms，
	// 线上机器约慢一倍；取 300 单让每批占用唯一写连接的时间在 100ms 上下。
	orderHistoryBatch = 300
)

// OrderHistoryPruner 删掉结束已超过 5 天的订单和它们的成交。1 分钟调仓的模拟组合一天几十万单，不清理库会一直涨。
//
// 只清理没开通现货、股票市场的模拟账户（例如纯永续模拟盘）：
//   - 现货、股票账户靠历史成交判断哪些资产归 MooX 管理（ManagedAssets），持仓均价也由全部成交现算，删了会改变行为；
//   - 实盘账户的成交按交易所成交号去重，成交游标回退时已删的成交会被重新入账。
//
// 这两类账户不清理（见交易模块文档的已知差别）。实现 FundMaintainer，由 FundWorker 定时运行；清理不是交易行为，不受暂停影响。
type OrderHistoryPruner struct {
	Store *store.Store
	Now   func() time.Time

	// batch 是每批删除的订单数，为 0 时取 orderHistoryBatch；只有测试会改。
	batch int
}

// RunAll 逐个可清理的账户分批删完过期订单。
func (p *OrderHistoryPruner) RunAll(ctx context.Context) error {
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	before := now.Add(-orderHistoryRetention).UnixMilli()
	batch := p.batch
	if batch <= 0 {
		batch = orderHistoryBatch
	}
	accounts, err := p.Store.ListAllTradingAccounts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, account := range accounts {
		if !orderHistoryPrunable(account) {
			continue
		}
		var fills int64
		orders, deleteErr := deleteInBatches(ctx, batch, func(limit int) (int64, error) {
			deletedOrders, deletedFills, err := p.Store.DeleteFinishedOrdersBefore(ctx, account.SpaceID, account.TradingAccountID, before, limit)
			fills += deletedFills
			return deletedOrders, err
		})
		if orders > 0 {
			log.Infof("交易：清理执行账户 %s/%s 结束超过 5 天的订单 %d 单、成交 %d 笔", account.SpaceID, account.TradingAccountID, orders, fills)
		}
		if deleteErr != nil {
			errs = append(errs, fmt.Errorf("清理执行账户 %s/%s 的历史订单：%w", account.SpaceID, account.TradingAccountID, deleteErr))
		}
	}
	return errors.Join(errs...)
}

// orderHistoryPrunable 报告账户的历史订单能否清理：只有模拟盘，且没开通持仓来自余额的市场。
func orderHistoryPrunable(account store.TradingAccountRecord) bool {
	return account.ExecutionMode == string(exchange.ExecutionModePaper) && !account.HasBalanceMarket()
}
