package fundmaint

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// 多稳定币保证金调平（币安统一账户）。
//   - 偏离的定义：各稳定币价值占稳定币总价值的实际比例与目标比例的绝对差，任何一个超过 5 个百分点就调平，否则不动；
//   - 买入（要增加的稳定币）用买一价限价单，到下一次检查还没成交就撤掉；卖出（多出来的稳定币）用 IOC；都在稳定币对 USDT 的现货标的上下单；
//   - 每轮只做一个方向：先卖多出来的换回 USDT，买入留到下一轮（卖单成交后余额才更新）；没成交的缺口下一次检查再补。

// stableOrderMaxAge 是稳定币买单最多挂多久：每小时检查一次，留 5 分钟余量，免得整点检查时刚好差几秒没撤。
const stableOrderMaxAge = 55 * time.Minute

type stableHolding struct {
	asset      string
	quantity   shared.Decimal
	price      shared.Decimal
	value      shared.Decimal
	instrument store.InstrumentRecord // USDT 本身没有
}

// stableApplies 报告稳定币调平是否适用于这个账户：只有币安统一账户（有现货钱包）。
func stableApplies(account store.TradingAccountRecord) bool {
	return account.AccountMode == string(exchange.AccountModeBinancePortfolioMargin) && account.HasBalanceMarket()
}

// settleStableOrders 处理上一轮留下的稳定币买单：挂满 stableOrderMaxAge（到下一次检查）还没成交的撤掉，
// 更早的唤醒（设置变更、会话就绪）不撤。hasOpen 为真表示还有没结束的单，这一轮不下新单。
// 它和调平分开做：BNB 补买让路时，过期的旧买单照样要撤。
func (s *Service) settleStableOrders(ctx context.Context, account store.TradingAccountRecord) (hasOpen bool, err error) {
	open, err := s.openSystemOrders(ctx, account, ownerStableRebalance)
	if err != nil || len(open) == 0 {
		return false, err
	}
	now := s.now()
	for _, record := range open {
		if now.Sub(time.UnixMilli(record.SubmittedAt)) < stableOrderMaxAge {
			continue
		}
		if _, err := s.Orders.Cancel(ctx, record.SpaceID, record.OrderID); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (s *Service) rebalanceStablecoins(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	settings fundsettings.Settings,
	account store.TradingAccountRecord,
	adapter execution.ExecutionAdapter,
) error {
	holdings, total, err := s.stableHoldings(ctx, settings, account, adapter)
	if err != nil {
		return err
	}
	if total.Cmp(shared.Zero()) <= 0 {
		return nil
	}
	deviated := false
	for _, holding := range holdings {
		actual := holding.value.Div(total)
		if actual.Sub(settings.StablecoinTargets[holding.asset]).Abs().Cmp(stableDeviation) > 0 {
			deviated = true
		}
	}
	if !deviated {
		return nil
	}
	type adjustment struct {
		holding stableHolding
		delta   shared.Decimal // 目标价值 − 当前价值：正为买入，负为卖出
	}
	var adjustments []adjustment
	for _, holding := range holdings {
		if strings.EqualFold(holding.asset, account.SettlementAsset) {
			continue
		}
		adjustments = append(adjustments, adjustment{holding: holding, delta: total.Mul(settings.StablecoinTargets[holding.asset]).Sub(holding.value)})
	}
	// 每轮只做一个方向：先卖。卖单成交后账户的余额要等同步才更新，同一轮的买单还按开始时的快照算，
	// 会因为资金不足被拒；买入留到下一轮（下一次检查，或卖单成交触发的账户就绪唤醒），不算失败。
	sort.SliceStable(adjustments, func(i, j int) bool { return adjustments[i].delta.Cmp(adjustments[j].delta) < 0 })
	var placeErrors []error
	soldThisRound := false
	for _, item := range adjustments {
		if item.delta.IsNegative() {
			placed, err := s.adjustStablecoin(ctx, logical, account, adapter, item.holding, item.delta)
			soldThisRound = soldThisRound || placed
			if err != nil {
				placeErrors = append(placeErrors, fmt.Errorf("%s：%w", item.holding.asset, err))
			}
		}
	}
	if soldThisRound || len(placeErrors) > 0 {
		return errors.Join(placeErrors...)
	}
	for _, item := range adjustments {
		if !item.delta.IsNegative() {
			if _, err := s.adjustStablecoin(ctx, logical, account, adapter, item.holding, item.delta); err != nil {
				placeErrors = append(placeErrors, fmt.Errorf("%s：%w", item.holding.asset, err))
			}
		}
	}
	return errors.Join(placeErrors...)
}

// stableHoldings 返回账户里各目标稳定币的持有量和折成结算币（USDT）的价值，以及合计。
// 稳定币的价格取它对 USDT 的现货标的参考价；USDT 自己价格为 1。没有对应标的的稳定币没法调平，整次跳过并告警。
func (s *Service) stableHoldings(
	ctx context.Context,
	settings fundsettings.Settings,
	account store.TradingAccountRecord,
	adapter execution.ExecutionAdapter,
) ([]stableHolding, shared.Decimal, error) {
	total := shared.Zero()
	holdings := make([]stableHolding, 0, len(settings.StablecoinTargets))
	assets := make([]string, 0, len(settings.StablecoinTargets))
	for asset := range settings.StablecoinTargets {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	for _, asset := range assets {
		holding := stableHolding{asset: asset, price: shared.MustDecimal("1")}
		for _, balance := range account.Snapshot.Balances {
			if !strings.EqualFold(balance.Asset, asset) {
				continue
			}
			quantity, err := shared.ParseDecimal(balance.Total)
			if err != nil {
				continue
			}
			if borrowed, borrowedErr := shared.ParseDecimal(balance.Borrowed); borrowedErr == nil {
				quantity = quantity.Sub(borrowed)
			}
			holding.quantity = holding.quantity.Add(quantity)
		}
		if !strings.EqualFold(asset, account.SettlementAsset) {
			instrument, found, err := s.spotInstrument(ctx, account, asset)
			if err != nil {
				return nil, shared.Zero(), err
			}
			if !found {
				s.warnOnce("stable-no-pair|"+account.TradingAccountID+"|"+asset, fmt.Sprintf("执行账户 %s 上没有 %s/%s 现货标的，稳定币保证金不调平", account.TradingAccountID, asset, account.SettlementAsset))
				return nil, shared.Zero(), nil
			}
			price, _, err := s.referencePrice(ctx, adapter, instrument)
			if err != nil {
				return nil, shared.Zero(), err
			}
			holding.instrument, holding.price = instrument, price
		}
		holding.value = holding.quantity.Mul(holding.price)
		total = total.Add(holding.value)
		holdings = append(holdings, holding)
	}
	return holdings, total, nil
}

// adjustStablecoin 为一种稳定币下调平单；买入用买一价限价 GTC（下一次检查没成交就撤），卖出用 IOC。
func (s *Service) adjustStablecoin(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	account store.TradingAccountRecord,
	adapter execution.ExecutionAdapter,
	holding stableHolding,
	delta shared.Decimal,
) (bool, error) {
	step, minimum, minNotional, err := quantityRules(holding.instrument, holding.price)
	if err != nil {
		return false, err
	}
	quantity := shared.FloorToStep(delta.Abs().Div(holding.price), step)
	if delta.IsNegative() && quantity.Cmp(holding.quantity) > 0 {
		quantity = shared.FloorToStep(holding.quantity, step)
	}
	if quantity.Cmp(minimum) < 0 || quantity.Mul(holding.price).Cmp(minNotional) < 0 {
		return false, nil
	}
	quote, err := adapter.GetQuote(ctx, exchange.NewSymbolKey(holding.instrument.MarketType, holding.instrument.ExchangeSymbol))
	if err != nil {
		return false, err
	}
	if quote.Bid.Cmp(shared.Zero()) <= 0 || quote.SourceTime.IsZero() || s.now().Sub(quote.SourceTime) > maxQuoteAge {
		return false, fmt.Errorf("买一价不可用或已过期")
	}
	bid := quote.Bid
	side, policy := exchange.SideBuy, exchange.FillPolicyGTC
	if delta.IsNegative() {
		// 卖出挂在买一价上吃单，IOC：没成交的部分直接作废，下一次检查再算。
		side, policy = exchange.SideSell, exchange.FillPolicyIOC
	}
	log.Infof("执行账户 %s 稳定币调平：%s %s %s @ %s", account.TradingAccountID, side, holding.instrument.InstrumentID, quantity.String(), bid.String())
	if err := s.placeSystemOrder(ctx, logical, account, holding.instrument, ownerStableRebalance, side, quantity, holding.price, quote.SourceTime, &bid, policy); err != nil {
		return false, err
	}
	return true, nil
}
