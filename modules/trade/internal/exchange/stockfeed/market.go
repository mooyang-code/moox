package stockfeed

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/packages/cnstock"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// quoteReuse 是同一标的快照的复用时间：海外机带宽小，不轮询全市场，2 秒内重复取价用缓存。
	quoteReuse = 2 * time.Second
	// listRefreshAt 是每天刷新标的列表的北京时间（A 股、港股开盘前；香港时间与北京时间相同）；listRetry 是刷新失败后的重试间隔。
	listRefreshAt = 8*time.Hour + 30*time.Minute
	listRetry     = 5 * time.Minute
)

var (
	// ErrListPending 表示标的列表还没有加载完（库里也没有），会话稍后重试。
	ErrListPending = errors.New("标的列表还没有加载完，稍后重试")
)

// ListSource 读场所完整的标的列表，已经换成带交易规则（每手股数、价格步长、结算资产）的标的。
type ListSource func(context.Context) ([]exchange.Instrument, error)

// QuoteSource 取一批股票的实时快照（按标的 ID 索引）。
type QuoteSource func(context.Context, []string) (map[string]cnstock.Quote, error)

// Seed 在标的列表第一次加载完之前，读库里已有的标的；进程重启后会话不必等标的列表。
type Seed func(context.Context) ([]exchange.Instrument, error)

// Market 是整个进程共用的一个股票场所的公共行情：快照缓存和标的列表各一份，这个场所的所有账户共用。
type Market struct {
	// Exchange 是场所；Label 是写进报错和日志的市场名（“A 股”“港股”）。
	Exchange exchange.Exchange
	Label    string
	// FillEmptyBook 为真时，快照里没有买一或卖一就用现价补上，不当作没有对手价。港股用：腾讯 r_hk 没有真实盘口，
	// 盘中买一、卖一字段都等于现价（2026-11-18 实测），为空时同样按现价撮合。有涨跌停的市场（A 股）不能开：没有卖一就是涨停。
	FillEmptyBook bool
	// QuoteTimeIsLastTrade 为真时，快照里的时间是这只标的最后一笔成交的时间，不是报价时间：报价时间改按取到这批快照的
	// 时刻算。港股用：腾讯 r_hk 第 30 字段是最后成交时间，成交稀疏的标的几十秒不变，按它判新鲜度会让换算、下单、估值都报过期
	// （2026-11-18 盘中实测）。A 股的同名字段随快照每 3 秒刷新，不能开。
	QuoteTimeIsLastTrade bool
	Quotes               QuoteSource
	List                 ListSource
	Seed                 Seed
	Now                  func() time.Time

	quoteMu sync.Mutex
	quotes  map[string]cachedQuote
	// failed 是最近一次取价失败的代码和失败原因：腾讯故障时 2 秒内不再逐只重试（每只都要等超时），直接返回同一个错误。
	failed map[string]failedQuote

	listMu      sync.RWMutex
	instruments []exchange.Instrument
}

type cachedQuote struct {
	quote     cnstock.Quote
	fetchedAt time.Time
}

type failedQuote struct {
	err error
	at  time.Time
}

func (m *Market) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// RunListRefresh 在后台刷新标的列表：启动时取一次，之后每天开盘前（08:30）取一次；失败沿用已有的列表，5 分钟后重试。
func (m *Market) RunListRefresh(ctx context.Context) {
	for {
		wait := listRetry
		if err := m.RefreshList(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.WarnContextf(ctx, "%s标的列表刷新失败，沿用已有列表：%v", m.Label, err)
		} else {
			wait = untilNextRefresh(m.now())
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// untilNextRefresh 返回到下一个北京时间 08:30 的时长。
func untilNextRefresh(now time.Time) time.Duration {
	local := now.In(time.FixedZone("CST", 8*60*60))
	next := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location()).Add(listRefreshAt)
	if !next.After(local) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(local)
}

// RefreshList 取一次完整的标的列表并替换缓存。
func (m *Market) RefreshList(ctx context.Context) error {
	if m.List == nil {
		return fmt.Errorf("%s标的列表来源未配置", m.Label)
	}
	instruments, err := m.List(ctx)
	if err != nil {
		return err
	}
	m.listMu.Lock()
	m.instruments = instruments
	m.listMu.Unlock()
	return nil
}

// LoadInstruments 返回缓存的标的列表。列表还没加载完时先用库里已有的；库里也没有（第一次启动）时报错，会话稍后重试。
func (m *Market) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	m.listMu.RLock()
	cached := append([]exchange.Instrument(nil), m.instruments...)
	m.listMu.RUnlock()
	if len(cached) > 0 {
		return cached, nil
	}
	if m.Seed != nil {
		seeded, err := m.Seed(ctx)
		if err != nil {
			return nil, err
		}
		if len(seeded) > 0 {
			return seeded, nil
		}
	}
	return nil, fmt.Errorf("%s%w", m.Label, ErrListPending)
}

// PrefetchQuotes 一次取回一批标的的快照（腾讯每次最多 60 只，超过分批），2 秒内取过的不再取；
// 2 秒内取价失败过的代码也不再请求，直接返回那次的错误。
func (m *Market) PrefetchQuotes(ctx context.Context, keys []exchange.SymbolKey) error {
	// 只读报价缓存的上下文（管理台查询）不取新快照。
	if _, only := exchange.CachedQuotesOnly(ctx); only {
		return nil
	}
	now := m.now()
	var missing []string
	var recentFailure error
	seen := make(map[string]struct{}, len(keys))
	m.quoteMu.Lock()
	for _, key := range keys {
		if key.Market != exchange.MarketTypeEquity {
			continue
		}
		if _, dup := seen[key.Symbol]; dup {
			continue
		}
		seen[key.Symbol] = struct{}{}
		if cached, ok := m.quotes[key.Symbol]; ok && now.Sub(cached.fetchedAt) <= quoteReuse {
			continue
		}
		if failure, ok := m.failed[key.Symbol]; ok && now.Sub(failure.at) <= quoteReuse {
			recentFailure = failure.err
			continue
		}
		missing = append(missing, key.Symbol)
	}
	m.quoteMu.Unlock()
	if len(missing) == 0 {
		return recentFailure
	}
	if m.Quotes == nil {
		return fmt.Errorf("%s行情来源未配置", m.Label)
	}
	fetched, err := m.Quotes(ctx, missing)
	// 缓存时间取请求返回之后：腾讯超时要等 5 秒才返回，用发请求前的时间写进去就已经过期。
	stamp := m.now()
	m.quoteMu.Lock()
	defer m.quoteMu.Unlock()
	if err != nil {
		err = fmt.Errorf("%s行情不可用：%w", m.Label, err)
		// 只有网络类错误（超时、错误状态码、限流）才记失败缓存；其余错误下次照常重试。
		if networkFailure(err) {
			if m.failed == nil {
				m.failed = make(map[string]failedQuote)
			}
			for _, id := range missing {
				m.failed[id] = failedQuote{err: err, at: stamp}
			}
		}
		return err
	}
	if m.quotes == nil {
		m.quotes = make(map[string]cachedQuote)
	}
	for _, id := range missing {
		delete(m.failed, id)
	}
	for id, quote := range fetched {
		m.quotes[id] = cachedQuote{quote: quote, fetchedAt: stamp}
	}
	return recentFailure
}

// networkFailure 报告取价失败是不是网络类错误：超时或连不上、错误状态码、限流。
func networkFailure(err error) bool {
	return errors.Is(err, cnstock.ErrTimeout) || errors.Is(err, cnstock.ErrHTTPStatus) || errors.Is(err, cnstock.ErrRateLimited)
}

func (m *Market) snapshot(ctx context.Context, key exchange.SymbolKey) (cachedQuote, error) {
	if key.Market != exchange.MarketTypeEquity {
		return cachedQuote{}, fmt.Errorf("%s只有股票市场，不支持 %s", m.Label, key.Market)
	}
	if err := m.PrefetchQuotes(ctx, []exchange.SymbolKey{key}); err != nil {
		return cachedQuote{}, err
	}
	m.quoteMu.Lock()
	cached, ok := m.quotes[key.Symbol]
	m.quoteMu.Unlock()
	if maxAge, only := exchange.CachedQuotesOnly(ctx); only && (!ok || m.now().Sub(cached.fetchedAt) > maxAge) {
		return cachedQuote{}, exchange.ErrQuoteNotCached
	}
	if !ok {
		return cachedQuote{}, &exchange.Error{Kind: exchange.ErrorRejected, Err: fmt.Errorf("腾讯行情没有标的 %s", key.Symbol)}
	}
	return cached, nil
}

// GetQuote 返回买一、卖一、现价、涨跌停价和停牌标记。停牌（含退市）的报价时间按取价时刻算、价格是昨收，不判过期。
func (m *Market) GetQuote(ctx context.Context, key exchange.SymbolKey) (execution.MarketQuote, error) {
	cached, err := m.snapshot(ctx, key)
	if err != nil {
		return execution.MarketQuote{}, err
	}
	return marketQuote(cached, m.FillEmptyBook, m.QuoteTimeIsLastTrade)
}

func marketQuote(cached cachedQuote, fillEmptyBook, quoteTimeIsLastTrade bool) (execution.MarketQuote, error) {
	quote := cached.quote
	parse := func(raw string) (shared.Decimal, error) {
		if raw == "" {
			return shared.Zero(), nil
		}
		return shared.ParseDecimal(raw)
	}
	values := make([]shared.Decimal, 6)
	for i, raw := range []string{quote.Bid, quote.Ask, quote.Last, quote.PrevClose, quote.LimitUp, quote.LimitDown} {
		value, err := parse(raw)
		if err != nil {
			return execution.MarketQuote{}, fmt.Errorf("腾讯行情 %s 的价格 %q 读不出来", quote.InstrumentID, raw)
		}
		values[i] = value
	}
	result := execution.MarketQuote{
		Bid: values[0], Ask: values[1], Last: values[2], SourceTime: quote.Time,
		LimitUp: values[4], LimitDown: values[5], Halted: quote.Halted || quote.Delisted,
	}
	if quoteTimeIsLastTrade {
		result.SourceTime = cached.fetchedAt.UTC()
	}
	if result.Last.IsZero() {
		// 当天还没有成交（开盘前）时现价为空，用昨收。
		result.Last = values[3]
	}
	if fillEmptyBook {
		if result.Bid.IsZero() {
			result.Bid = result.Last
		}
		if result.Ask.IsZero() {
			result.Ask = result.Last
		}
	}
	if result.Halted {
		result.Bid, result.Ask = shared.Zero(), shared.Zero()
		result.Last = values[3]
		result.SourceTime = cached.fetchedAt.UTC()
	}
	if result.Last.Cmp(shared.Zero()) <= 0 {
		return execution.MarketQuote{}, fmt.Errorf("腾讯行情 %s 没有价格", quote.InstrumentID)
	}
	return result, nil
}

// GetReferencePrice 用现价做参考价（停牌时是昨收）。
func (m *Market) GetReferencePrice(ctx context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	quote, err := m.GetQuote(ctx, key)
	if err != nil {
		return exchange.ReferencePrice{}, err
	}
	return exchange.ReferencePrice{Price: quote.Last, UpdatedAt: quote.SourceTime}, nil
}

// Adapter 是一个股票场所绑定的公共行情适配器：行情、参考价、标的都来自共用的 Market，只实现公共行情接口。
type Adapter struct {
	market *Market
}

var (
	_ execution.PublicMarketData = (*Adapter)(nil)
	_ execution.ExchangeIdentity = (*Adapter)(nil)
	_ execution.QuotePrefetcher  = (*Adapter)(nil)
)

// NewAdapter 返回绑定到共用行情的适配器。
func NewAdapter(market *Market) *Adapter { return &Adapter{market: market} }

func (a *Adapter) Exchange() exchange.Exchange { return a.market.Exchange }

func (a *Adapter) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	return a.market.LoadInstruments(ctx)
}

func (a *Adapter) GetQuote(ctx context.Context, key exchange.SymbolKey) (execution.MarketQuote, error) {
	return a.market.GetQuote(ctx, key)
}

func (a *Adapter) GetReferencePrice(ctx context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	return a.market.GetReferencePrice(ctx, key)
}

func (a *Adapter) PrefetchQuotes(ctx context.Context, keys []exchange.SymbolKey) error {
	return a.market.PrefetchQuotes(ctx, keys)
}
