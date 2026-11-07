package store

import (
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// MinNotionalAt 返回在某个价格下的最小下单名义金额（十进制字符串，没有下限返回空串），规则同 exchange.Instrument.MinNotionalAt：
// 交易所给了最小名义金额（币安）就用它；没有（OKX）用 最小下单量 × 合约面值 × 价格 折算。
// 数量、合约面值解析不了时返回错误，由调用方按无效规则处理。
func (r InstrumentRecord) MinNotionalAt(price shared.Decimal) (string, error) {
	if strings.TrimSpace(r.MinNotional) != "" {
		minimum, err := shared.ParseDecimal(r.MinNotional)
		if err != nil {
			return "", err
		}
		if minimum.Cmp(shared.Zero()) > 0 {
			return r.MinNotional, nil
		}
	}
	if price.Cmp(shared.Zero()) <= 0 || strings.TrimSpace(r.MinExchangeQuantity) == "" {
		return "", nil
	}
	quantity, err := shared.ParseDecimal(r.MinExchangeQuantity)
	if err != nil {
		return "", err
	}
	if quantity.Cmp(shared.Zero()) <= 0 {
		return "", nil
	}
	contractValue := shared.MustDecimal("1")
	if strings.TrimSpace(r.ContractValue) != "" {
		parsed, err := shared.ParseDecimal(r.ContractValue)
		if err != nil {
			return "", err
		}
		if parsed.Cmp(shared.Zero()) > 0 {
			contractValue = parsed
		}
	}
	return quantity.Mul(contractValue).Mul(price).String(), nil
}
