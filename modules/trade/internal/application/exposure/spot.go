// Package exposure 统一现货受管资产“卖哪个标的、剩下的算不算零头”的判断，清仓和移除成员共用，口径必须一致。
package exposure

import (
	"math/big"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// SpotInstruments 返回现货资产到卖出标的的对应：计价资产等于结算资产、在结算资产下可交易；同一资产取第一个。
func SpotInstruments(instruments []store.InstrumentRecord, settlementAsset string) map[string]store.InstrumentRecord {
	byAsset := make(map[string]store.InstrumentRecord)
	for _, instrument := range instruments {
		if !Tradable(instrument, settlementAsset) ||
			instrument.QuoteAsset != settlementAsset ||
			instrument.BaseAsset == settlementAsset {
			continue
		}
		if _, exists := byAsset[instrument.BaseAsset]; !exists {
			byAsset[instrument.BaseAsset] = instrument
		}
	}
	return byAsset
}

// Tradable 判断标的在该结算资产下是否可交易。
func Tradable(instrument store.InstrumentRecord, settlementAsset string) bool {
	return (instrument.Status == "TRADING" || instrument.Status == "live") &&
		(instrument.SettlementAsset == "" || instrument.SettlementAsset == settlementAsset)
}

// SpotDust 判断现货余额总量是不是下不了单的零头：按步长取整后低于最小数量，
// 或者按价格低于最小名义金额。price 不大于 0 表示拿不到报价，只比较最小数量。
// 规则本身解析不了时不算零头，交给调用方按剩余仓位处理。
func SpotDust(total, price shared.Decimal, instrument store.InstrumentRecord) bool {
	step, err := shared.ParseDecimal(instrument.ExchangeQuantityStep)
	if err != nil || step.Cmp(shared.Zero()) <= 0 {
		return false
	}
	minimum, err := decimalOrZero(instrument.MinExchangeQuantity)
	if err != nil {
		return false
	}
	floored := FloorToStep(total, step)
	if floored.IsZero() || floored.Cmp(minimum) < 0 {
		return true
	}
	if price.Cmp(shared.Zero()) <= 0 {
		return false
	}
	minNotional, err := decimalOrZero(instrument.MinNotional)
	if err != nil {
		return false
	}
	return minNotional.Cmp(shared.Zero()) > 0 && floored.Mul(price).Cmp(minNotional) < 0
}

// FloorToStep 把数量向下取整到步长的整数倍。
func FloorToStep(value, step shared.Decimal) shared.Decimal {
	if value.Cmp(shared.Zero()) <= 0 || step.Cmp(shared.Zero()) <= 0 {
		return shared.Zero()
	}
	ratio := new(big.Rat)
	if _, ok := ratio.SetString(value.Div(step).String()); !ok {
		return shared.Zero()
	}
	units := new(big.Int).Quo(ratio.Num(), ratio.Denom())
	whole, err := shared.ParseDecimal(units.String())
	if err != nil {
		return shared.Zero()
	}
	return whole.Mul(step)
}

func decimalOrZero(raw string) (shared.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return shared.Zero(), nil
	}
	return shared.ParseDecimal(raw)
}
