package target

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

type AdapterSource interface {
	Adapter(tradingAccountID string) (execution.ExecutionAdapter, error)
}

// Quote 是执行器用的报价。有涨跌停的市场（A 股）还带着涨停价、跌停价、买一卖一和停牌标记，其余市场这几项为零值。
type Quote struct {
	Price     shared.Decimal
	UpdatedAt time.Time
	// LimitUp、LimitDown 是涨停价、跌停价（绝对价）；零值表示没有。
	LimitUp   shared.Decimal
	LimitDown shared.Decimal
	// Bid、Ask 是买一、卖一价，只在有涨跌停的市场里取：涨停没有卖一时买不进，跌停没有买一时卖不出。
	Bid    shared.Decimal
	Ask    shared.Decimal
	Halted bool
}

type PriceSource interface {
	LatestPrice(context.Context, string, exchange.SymbolKey) (Quote, error)
}

// QuotePrefetchSource 是能按账户批量预取报价的报价来源（ExchangePriceSource 实现）。
type QuotePrefetchSource interface {
	PrefetchQuotes(ctx context.Context, tradingAccountID string, keys []exchange.SymbolKey)
}

type ExchangePriceSource struct {
	Adapters AdapterSource
	// Exchanges 提供场所的交易时段，买一卖一的新鲜度按它判断；为空时全天按时段内处理。
	Exchanges execution.ExchangeCatalog
	Now       func() time.Time
}

func (s ExchangePriceSource) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s ExchangePriceSource) LatestPrice(
	ctx context.Context,
	tradingAccountID string,
	key exchange.SymbolKey,
) (Quote, error) {
	if s.Adapters == nil {
		return Quote{}, ErrExecutorConfig
	}
	adapter, err := s.Adapters.Adapter(tradingAccountID)
	if err != nil {
		return Quote{}, err
	}
	if exchange.RuleFor(key.Market).PriceLimits {
		// 有涨跌停的市场一次取全：参考价、涨跌停价、买一卖一、停牌都来自同一份快照。
		quote, err := adapter.GetQuote(ctx, key)
		if err != nil {
			return Quote{}, err
		}
		return Quote{
			Price: quote.Last, UpdatedAt: quote.SourceTime, LimitUp: quote.LimitUp, LimitDown: quote.LimitDown,
			Bid: quote.Bid, Ask: quote.Ask, Halted: quote.Halted,
		}, nil
	}
	quote, err := adapter.GetReferencePrice(ctx, key)
	if err != nil {
		return Quote{}, err
	}
	return Quote{Price: quote.Price, UpdatedAt: quote.UpdatedAt}, nil
}

// PrefetchQuotes 在适配器支持时批量预取报价；失败不报错，之后逐个取价时照常处理。
func (s ExchangePriceSource) PrefetchQuotes(ctx context.Context, tradingAccountID string, keys []exchange.SymbolKey) {
	if s.Adapters == nil {
		return
	}
	adapter, err := s.Adapters.Adapter(tradingAccountID)
	if err != nil {
		return
	}
	execution.PrefetchQuotes(ctx, adapter, keys)
}

// exchangeOf 返回适配器所属的场所名；适配器没有登记身份时返回空串（按全天交易处理）。
func exchangeOf(adapter any) string {
	if identity, ok := adapter.(execution.ExchangeIdentity); ok {
		return string(identity.Exchange())
	}
	return ""
}
