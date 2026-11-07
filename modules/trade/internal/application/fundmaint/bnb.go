package fundmaint

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// bnbQuantity 是账户各钱包里 BNB 的净数量（现货和合约钱包都可能有，抵扣手续费两边都用）。
func bnbQuantity(account store.TradingAccountRecord) shared.Decimal {
	total := shared.Zero()
	for _, balance := range account.Snapshot.Balances {
		if !strings.EqualFold(balance.Asset, fundsettings.BNBAsset) {
			continue
		}
		quantity, err := shared.ParseDecimal(balance.Total)
		if err != nil {
			continue
		}
		if borrowed, borrowedErr := shared.ParseDecimal(balance.Borrowed); borrowedErr == nil {
			quantity = quantity.Sub(borrowed)
		}
		total = total.Add(quantity)
	}
	return total
}

// topUpBNB 在 BNB 市值低于“保留金额 − 10”时市价补买到保留金额（SYSTEM 单，现货下单）。
// 普通账户把补买的 BNB 在现货、合约钱包之间分配需要钱包划转，由真实交易所接入阶段的划转实现负责；
// 这里只把缺口买到现货钱包里。账户没开通现货时无法补买，告警一次。
func (s *Service) topUpBNB(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	settings fundsettings.Settings,
	account store.TradingAccountRecord,
	adapter execution.ExecutionAdapter,
) error {
	reserve := settings.BNBReserveAmount
	if reserve.Cmp(shared.Zero()) <= 0 {
		return nil
	}
	if !account.HasBalanceMarket() {
		s.warnOnce("bnb-no-spot|"+account.TradingAccountID, fmt.Sprintf("执行账户 %s 没有开通现货，无法补买 BNB 保留金额", account.TradingAccountID))
		return nil
	}
	instrument, found, err := s.spotInstrument(ctx, account, fundsettings.BNBAsset)
	if err != nil {
		return err
	}
	if !found {
		s.warnOnce("bnb-no-instrument|"+account.TradingAccountID, fmt.Sprintf("执行账户 %s 上没有 BNB/%s 现货标的，无法补买", account.TradingAccountID, account.SettlementAsset))
		return nil
	}
	open, err := s.openSystemOrders(ctx, account, ownerBNBReserve)
	if err != nil {
		return err
	}
	if len(open) > 0 {
		return nil
	}
	price, quotedAt, err := s.referencePrice(ctx, adapter, instrument)
	if err != nil {
		return err
	}
	value := bnbQuantity(account).Mul(price)
	if value.Cmp(reserve.Sub(fundsettings.BNBReserveSlack)) >= 0 {
		return nil
	}
	step, minimum, minNotional, err := quantityRules(instrument)
	if err != nil {
		return err
	}
	quantity := shared.FloorToStep(reserve.Sub(value).Div(price), step)
	if quantity.Cmp(minimum) < 0 || quantity.Mul(price).Cmp(minNotional) < 0 {
		s.warnOnce("bnb-too-small|"+account.TradingAccountID, fmt.Sprintf("执行账户 %s 的 BNB 补买量低于交易所最小下单量，暂不补买", account.TradingAccountID))
		return nil
	}
	return s.placeSystemOrder(ctx, logical, account, instrument, ownerBNBReserve, exchange.SideBuy, quantity, price, quotedAt, nil, exchange.FillPolicyUnspecified)
}
