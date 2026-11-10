package fundmaint

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/fundsettings"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// isStandardAccount 报告账户是否是币安普通账户（现货、合约两个钱包分开）。
func isStandardAccount(account store.TradingAccountRecord) bool {
	return exchange.AccountMode(account.AccountMode) == exchange.AccountModeBinanceStandard
}

// bnbQuantity 是补买维护的那部分 BNB 的净数量：普通账户只数现货钱包（补买只买进现货钱包；币安 U 本位合约的 BNB 抵扣要用合约钱包里的 BNB，
// 补买不给合约钱包补，合约钱包里有多少是用户自己放的，不算）；统一账户只有一个钱包，全数。借出的不算。
func bnbQuantity(account store.TradingAccountRecord) shared.Decimal {
	standard := isStandardAccount(account)
	total := shared.Zero()
	for _, balance := range account.Snapshot.Balances {
		if !strings.EqualFold(balance.Asset, fundsettings.BNBAsset) || (standard && balance.Wallet != string(exchange.WalletSpot)) {
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

// bnbBounds 返回补买的目标市值和触发容差：普通账户只补现货钱包，目标是保留金额的一半（和归集时现货钱包的留存口径一致），容差同样减半
// （保留金额大于 10 时目标减容差仍为正）；统一账户一个钱包，取全额。BNB 市值低于“目标 − 容差”才补买，买到目标。
func bnbBounds(account store.TradingAccountRecord, reserve shared.Decimal) (target, slack shared.Decimal) {
	target, slack = reserve, fundsettings.BNBReserveSlack
	if isStandardAccount(account) {
		half := shared.MustDecimal("2")
		target, slack = target.Div(half), slack.Div(half)
	}
	return target, slack
}

// topUpBNB 在 BNB 市值低于“目标 − 容差”时市价补买到目标（SYSTEM 单，现货下单），目标和容差见 bnbBounds：
// 普通账户只补现货钱包、目标是保留金额的一半，不在钱包之间划转 BNB；统一账户目标是保留金额。账户没开通现货时无法补买，告警一次。
// busy 为真表示补买单刚下出或还没结束：买单要花 USDT、余额要等同步才更新，同一轮后面的稳定币调平先让路。
func (s *Service) topUpBNB(
	ctx context.Context,
	logical store.LogicalAccountRecord,
	settings fundsettings.Settings,
	account store.TradingAccountRecord,
	adapter execution.ExecutionAdapter,
) (busy bool, err error) {
	reserve := settings.BNBReserveAmount
	if reserve.Cmp(shared.Zero()) <= 0 {
		return false, nil
	}
	if !account.HasBalanceMarket() {
		s.warnOnce("bnb-no-spot|"+account.TradingAccountID, fmt.Sprintf("执行账户 %s 没有开通现货，无法补买 BNB 保留金额", account.TradingAccountID))
		return false, nil
	}
	instrument, found, err := s.spotInstrument(ctx, account, fundsettings.BNBAsset)
	if err != nil {
		return false, err
	}
	if !found {
		s.warnOnce("bnb-no-instrument|"+account.TradingAccountID, fmt.Sprintf("执行账户 %s 上没有 BNB/%s 现货标的，无法补买", account.TradingAccountID, account.SettlementAsset))
		return false, nil
	}
	open, err := s.openSystemOrders(ctx, account, ownerBNBReserve)
	if err != nil {
		return false, err
	}
	if len(open) > 0 {
		// 待提交的旧单直接丢弃；提交结果未知的先向交易所解析。
		resolved := false
		for _, record := range open {
			if state := orderdomain.State(record.State); state == orderdomain.Submitting || state == orderdomain.SubmitUnknown {
				resolved = true
			}
			if err := s.advanceUnconfirmed(ctx, record); err != nil {
				return true, err
			}
		}
		// 解析过旧单的这一轮不下新单：解析出的成交还没进账户快照，按旧快照算缺口会重复补买。
		if resolved {
			return true, nil
		}
		if open, err = s.openSystemOrders(ctx, account, ownerBNBReserve); err != nil {
			return true, err
		}
		if len(open) > 0 {
			return true, nil
		}
	}
	price, quotedAt, err := s.referencePrice(ctx, adapter, instrument)
	if err != nil {
		return false, err
	}
	target, slack := bnbBounds(account, reserve)
	value := bnbQuantity(account).Mul(price)
	if value.Cmp(target.Sub(slack)) >= 0 {
		return false, nil
	}
	step, minimum, minNotional, err := quantityRules(instrument, price)
	if err != nil {
		return false, err
	}
	quantity := shared.FloorToStep(target.Sub(value).Div(price), step)
	if quantity.Cmp(minimum) < 0 || quantity.Mul(price).Cmp(minNotional) < 0 {
		s.warnOnce("bnb-too-small|"+account.TradingAccountID, fmt.Sprintf("执行账户 %s 的 BNB 补买量低于交易所最小下单量，暂不补买", account.TradingAccountID))
		return false, nil
	}
	if err := s.placeSystemOrder(ctx, logical, account, instrument, ownerBNBReserve, exchange.SideBuy, quantity, price, quotedAt, nil, exchange.FillPolicyUnspecified); err != nil {
		return false, err
	}
	return true, nil
}
