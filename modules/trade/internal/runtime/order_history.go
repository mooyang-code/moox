package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// orderHistoryBatch 是每批删除的订单数（连同它们的成交）。删之前要先读这批成交累加进小时汇总表：
	// 本地 30 万单、45 万笔成交的库上每批 300 单平均约 27ms（中位 16ms、最慢 75ms），不汇总时约 15ms；
	// 500 单平均约 41ms、最慢 114ms。线上机器约慢一倍，300 单让每批占用唯一写连接的时间在 100ms 以内。
	orderHistoryBatch = 300
)

// OrderHistoryPruner 删掉结束已超过保留天数的订单和它们的成交。1 分钟调仓的模拟组合一天几十万单，不清理库会一直涨。
// 保留天数是各模拟账户自己的配置（t_paper_account_configs.c_order_retention_days，默认 5 天，0 表示不清理），
// 每轮按账户分别算边界。删掉的成交的手续费、已实现盈亏和笔数先按小时汇总，收益拆解不受影响（见 store.DeleteFinishedOrdersBefore）。
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
		days := orderRetentionDays(account)
		if days <= 0 {
			continue
		}
		before := now.Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
		if account.SupportsMarket(string(exchange.MarketTypeSwap)) {
			// 模拟资金费补算从最近一次结算所在整点的前一小时起，用之后的成交倒推结算时刻的持仓（paper.Adapter.SettleFunding），
			// 这段成交不能删：边界不晚于补算起点。订单结束时间不早于它的成交时间，按结束时间删就不会删到补算要读的成交。
			latest, err := p.Store.LatestPaperFundingSettlement(ctx, account.SpaceID, account.TradingAccountID)
			if err != nil {
				errs = append(errs, fmt.Errorf("读取执行账户 %s/%s 的最近资金费结算：%w", account.SpaceID, account.TradingAccountID, err))
				continue
			}
			if latest > 0 {
				// 补算最多回看 paper.FundingBackfillLimit：资金费长期结算失败时边界也只停在那里，不让库一直涨。
				start := time.UnixMilli(latest).UTC().Truncate(time.Hour).Add(-time.Hour).UnixMilli()
				before = min(before, max(start, now.Add(-paper.FundingBackfillLimit).UnixMilli()))
			}
		}
		var fills int64
		orders, deleteErr := deleteInBatches(ctx, batch, func(limit int) (int64, error) {
			deletedOrders, deletedFills, err := p.Store.DeleteFinishedOrdersBefore(ctx, account.SpaceID, account.TradingAccountID, before, limit)
			fills += deletedFills
			return deletedOrders, err
		})
		if orders > 0 {
			log.Infof("交易：清理执行账户 %s/%s 结束超过 %d 天的订单 %d 单、成交 %d 笔", account.SpaceID, account.TradingAccountID, days, orders, fills)
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

// orderRetentionDays 取账户配置的保留天数；读不到模拟配置时按默认值。
func orderRetentionDays(account store.TradingAccountRecord) int {
	if account.PaperConfig == nil {
		return tradingaccount.DefaultOrderRetentionDays
	}
	return account.PaperConfig.OrderRetentionDays
}
