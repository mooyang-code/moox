package execution

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// 受管现货取不到新鲜报价时的估值规则，权益采样（application/equity）和模拟盘账户快照（execution/paper）共用：
//  1. 有不超龄的最后已知价就用它；
//  2. 否则只有三种情况按 0 计：标的表里没有这个标的、交易所确认标的不存在、标的已不在交易状态又在下架名单里——
//     这些资产已经卖不掉或很快归零；
//  3. 标的不在交易状态（BREAK、HALT、暂停、盘前等）但仍可能恢复：退回这个资产在本账户最近一笔成交的价格；
//  4. 仍然没有价，或者标的还在交易、只是这次取价失败（限流、网络、报价过期）：照旧报错，不把真实持仓按 0 计
//     （权益骤降会让目标换算去缩仓）。标的在下架名单里但还在交易时也是这样：下架之前它仍有市价。

// ValuationFailure 是取不到新鲜报价的原因。
type ValuationFailure int

const (
	// ValuationMissingInstrument：标的表里没有这个资产对结算币的现货标的。
	ValuationMissingInstrument ValuationFailure = iota + 1
	// ValuationDelisted：标的已不在交易状态，又在下架名单里。
	ValuationDelisted
	// ValuationInstrumentGone：交易所确认标的不存在（币安 -1121、OKX 51001）。
	ValuationInstrumentGone
	// ValuationNotTrading：标的状态不是可交易（BREAK、HALT、暂停、盘前等），不发取价请求。
	ValuationNotTrading
	// ValuationQuoteFailed：标的仍在交易，这次取价失败。
	ValuationQuoteFailed
)

// ZeroAllowed 报告这个原因下没有价格时能否按 0 计。
func (f ValuationFailure) ZeroAllowed() bool {
	return f == ValuationMissingInstrument || f == ValuationDelisted || f == ValuationInstrumentGone
}

// LastPriceMaxAge 是最后已知价的保留时间，和权益采样的过期阈值一致（equity.Service 的默认 SourceMaxAge）：
// 超过它的最后已知价不再拿来估值，否则一次长时间限流会让旧价格冒充新鲜的权益。
const LastPriceMaxAge = 90 * time.Second

// LastPrice 是最近一次取到的新鲜报价和取价时间。
type LastPrice struct {
	Price shared.Decimal
	At    time.Time
}

// usable 报告最后已知价在 now 时是否还能用。
func (p LastPrice) usable(now time.Time, maxAge time.Duration) bool {
	if p.At.IsZero() || p.Price.Cmp(shared.Zero()) <= 0 {
		return false
	}
	age := now.Sub(p.At)
	return age <= maxAge && age >= -maxAge
}

// DegradedValue 是降级估值的结果；How 是给关注项和日志看的说法（“按最后已知价计”等）。
type DegradedValue struct {
	Price shared.Decimal
	How   string
}

// LastFillPrice 返回这个资产在本账户最近一笔成交的价格；没有成交时 found 为假。
type LastFillPrice func(context.Context) (price shared.Decimal, found bool, err error)

// DegradedPrice 按文件开头的规则给一笔取不到新鲜报价的受管现货定价。cause 是取不到价的原因，报错时原样返回
// （标的仍在交易时就是取价错误本身，调用方可以按错误类型判断）。
func DegradedPrice(
	ctx context.Context,
	failure ValuationFailure,
	last LastPrice,
	now time.Time,
	maxAge time.Duration,
	lastFill LastFillPrice,
	cause error,
) (DegradedValue, error) {
	if last.usable(now, maxAge) {
		return DegradedValue{Price: last.Price, How: "按最后已知价计"}, nil
	}
	if failure.ZeroAllowed() {
		return DegradedValue{Price: shared.Zero(), How: "按 0 计"}, nil
	}
	if failure == ValuationNotTrading && lastFill != nil {
		price, found, err := lastFill(ctx)
		if err != nil {
			return DegradedValue{}, err
		}
		if found && price.Cmp(shared.Zero()) > 0 {
			return DegradedValue{Price: price, How: "按最近一笔成交价计"}, nil
		}
		return DegradedValue{}, fmt.Errorf("无法估值：%w，没有最后已知价，也没有成交记录", cause)
	}
	return DegradedValue{}, cause
}
