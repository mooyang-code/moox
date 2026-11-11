package operator

import (
	"context"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/exposure"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 清仓剩余统计：一轮结束后还剩哪些持仓、哪些是零头。

// reasonArbitrageHedgeLeg 是只平一个标的而它正是套利底仓对冲腿时的剩余原因。
const reasonArbitrageHedgeLeg = "套利底仓的永续对冲腿，不自动平仓，需要人工处理"

func (s *Service) remainingForAccount(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	reasons map[string]flattenReason,
	result *FlattenAccountResult,
	scope flattenScope,
) []RemainingPosition {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	account, err := s.Store.GetTradingAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		result.addError(err)
		return []RemainingPosition{{
			Quantity: "unknown", Reason: err.Error(),
		}}
	}
	var remaining []RemainingPosition
	instruments, listErr := s.Store.ListInstrumentsForAccount(ctx, tradingAccountID)
	if listErr != nil {
		result.addError(listErr)
		return []RemainingPosition{{Quantity: "unknown", Reason: listErr.Error()}}
	}
	// 标的表索引和平仓用同一份：持仓所属标的按（市场，代码）查表，不用持仓记录里可能对不上的标的 ID。
	index := newInstrumentIndex(instruments, account.SettlementAsset)
	settings := scope.settings
	if account.HasPositionMarket() {
		positions, listErr := s.Store.ListPositions(
			ctx, spaceID, tradingAccountID, exchange.SymbolKey{},
		)
		if listErr != nil {
			result.addError(listErr)
			return []RemainingPosition{{
				Quantity: "unknown", Reason: listErr.Error(),
			}}
		}
		for _, position := range positions {
			quantity, parseErr := decimalOrZero(position.SignedQuantity)
			if parseErr != nil {
				result.addError(parseErr)
				continue
			}
			if quantity.IsZero() {
				continue
			}
			instrumentID := ""
			if instrument, found := index.bySymbol[exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol)]; found {
				if settings.IsArbitrageBase(instrument.BaseAsset) {
					// 对冲腿不算整组清仓和减仓的剩余；只平这个标的而它正是对冲腿时，要作为剩余报出来。
					if scope.instrumentID == "" || !scope.includes(instrument.InstrumentID) {
						continue
					}
					remaining = append(remaining, RemainingPosition{
						InstrumentID: instrument.InstrumentID, Quantity: quantity.String(), Reason: reasonArbitrageHedgeLeg, final: true,
					})
					continue
				}
				instrumentID = instrument.InstrumentID
			} else if scope.narrow() {
				continue // 标的表里没有它，收窄范围时不算范围内
			} else {
				instrumentID = position.ExchangeSymbol
			}
			if !scope.includes(instrumentID) {
				continue
			}
			target, inScope := scope.target(tradingAccountID, instrumentID)
			if !inScope {
				continue
			}
			// 减仓时剩余指超出目标数量的部分。
			quantity = excess(quantity, target)
			if quantity.IsZero() {
				continue
			}
			reason := reasonFor(reasons, exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol).String(), "最后一次同步后仍有持仓")
			remaining = append(remaining, RemainingPosition{
				InstrumentID: instrumentID, Quantity: quantity.String(), Reason: reason.text, final: reason.final,
			})
		}
	}
	if account.HasBalanceMarket() {
		// 读取失败时 managed 为空，下面的现货循环全部跳过；已算出的合约剩余照常返回，不能让结果看起来像平完了。
		managed, managedErr := s.Store.ManagedAssets(ctx, spaceID, tradingAccountID)
		if managedErr != nil {
			result.addError(managedErr)
		}
		result.Dust = nil
		for _, balance := range account.SpotBalances() {
			if _, ok := managed[balance.Asset]; !ok || balance.Asset == account.SettlementAsset {
				continue
			}
			quantity, parseErr := decimalOrZero(balance.Total)
			if parseErr != nil {
				result.addError(parseErr)
				continue
			}
			if quantity.Cmp(shared.Zero()) <= 0 {
				continue
			}
			instrument, found := index.spot[balance.Asset]
			if !found && scope.narrow() {
				continue // 没有对应的现货标的，收窄范围时不算范围内
			}
			if found {
				if !scope.includes(instrument.InstrumentID) {
					continue
				}
				target, inScope := scope.target(tradingAccountID, instrument.InstrumentID)
				if !inScope {
					continue
				}
				quantity = excess(quantity, target)
				if quantity.Cmp(shared.Zero()) <= 0 {
					continue
				}
				// 只用本轮已拿到的报价；没有报价时只按最小数量判断，收尾不再发请求。
				if exposure.Tradable(instrument, account.SettlementAsset) &&
					exposure.SpotDust(quantity, result.quotes[balance.Asset], instrument) {
					result.Dust = append(result.Dust, RemainingPosition{
						Asset: balance.Asset, Quantity: quantity.String(), Reason: "零头，低于交易所最小数量或最小金额",
					})
					continue
				}
			}
			reason := reasonFor(
				reasons, "asset:"+balance.Asset, "最后一次同步后仍有余额",
			)
			remaining = append(remaining, RemainingPosition{
				Asset: balance.Asset, Quantity: quantity.String(), Reason: reason.text, final: reason.final,
			})
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].InstrumentID+remaining[i].Asset <
			remaining[j].InstrumentID+remaining[j].Asset
	})
	return remaining
}

func reasonFor(values map[string]flattenReason, key, fallback string) flattenReason {
	if reason := values[key]; reason.text != "" {
		return reason
	}
	return flattenReason{text: fallback}
}
