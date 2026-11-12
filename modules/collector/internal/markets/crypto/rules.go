// Package crypto 是加密货币市场的标的规则，包初始化时登记到 markets。
package crypto

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	cryptoid "github.com/mooyang-code/moox/modules/collector/internal/sources/crypto"
)

// MarketID 是加密货币市场的空间 ID。
const MarketID = "crypto"

func init() { markets.Register(MarketID, Rules{}) }

// Rules 实现 markets.SubjectRules。
type Rules struct{}

// DefaultInstrumentType：没写品种时是现货，marketType 为 swap 时是永续。
func (Rules) DefaultInstrumentType(marketType string) marketdata.InstrumentType {
	if strings.EqualFold(strings.TrimSpace(marketType), string(marketdata.InstrumentSwap)) {
		return marketdata.InstrumentSwap
	}
	return marketdata.InstrumentSpot
}

// ParseSubject 把 BTC-USDT（可带 -SPOT、-SWAP 后缀）换成交易所的 symbol BTCUSDT；后缀与品种不符的报错。
func (Rules) ParseSubject(instrumentType marketdata.InstrumentType, subjectID string) (markets.Subject, error) {
	if instrumentType != marketdata.InstrumentSpot && instrumentType != marketdata.InstrumentSwap {
		return markets.Subject{}, fmt.Errorf("%w: 加密货币不支持品种 %q", marketdata.ErrUnsupportedSymbol, instrumentType)
	}
	value := strings.ToUpper(strings.TrimSpace(subjectID))
	wantSuffix := "-" + strings.ToUpper(string(instrumentType))
	for _, suffix := range []string{"-SPOT", "-SWAP"} {
		if strings.HasSuffix(value, suffix) && suffix != wantSuffix {
			return markets.Subject{}, fmt.Errorf("%w: 标的 %s 的后缀 %s 与品种 %s 不符", marketdata.ErrUnsupportedSymbol, subjectID, suffix, instrumentType)
		}
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(value, "-SPOT"), "-SWAP"), "-")
	if len(parts) != 2 {
		return markets.Subject{}, fmt.Errorf("%w: 标的 %s 不是 BASE-QUOTE 形式", marketdata.ErrUnsupportedSymbol, subjectID)
	}
	if _, err := cryptoid.SubjectID(parts[0], parts[1]); err != nil {
		return markets.Subject{}, err
	}
	return markets.Subject{Code: parts[0] + parts[1]}, nil
}

// SubjectAttributes：币种取标的的计价币；永续合约带上交易对。
func (Rules) SubjectAttributes(item marketdata.Instrument) map[string]string {
	values := map[string]string{
		"subject_type": "crypto_pair", "market": "CRYPTO", "currency": strings.ToUpper(item.QuoteAsset),
		"base": strings.ToUpper(item.BaseAsset), "quote": strings.ToUpper(item.QuoteAsset),
	}
	// 永续合约的交易对：WS 连续合约 K 线按它订阅（目前 USDT 永续与 symbol 相同），采集计划从这个属性带出 pair。
	if pair := strings.ToUpper(strings.TrimSpace(item.Pair)); pair != "" {
		values["pair"] = pair
	}
	return values
}
