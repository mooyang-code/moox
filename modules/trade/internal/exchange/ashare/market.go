package ashare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	// SettlementAsset 是 A 股的计价和结算资产。
	SettlementAsset = "CNY"
	// listRefreshAt 是每天刷新标的列表的北京时间（开盘前）；listRetry 是刷新失败后的重试间隔。
	listRefreshAt = 8*time.Hour + 30*time.Minute
	listRetry     = 5 * time.Minute
)

var (
	priceTick      = shared.MustDecimal("0.01")
	cageBuyRatio   = shared.MustDecimal("1.02")
	cageSellRatio  = shared.MustDecimal("0.98")
	errListPending = errors.New("A 股标的列表还没有加载完，稍后重试")
	// ErrNoOrders 表示 A 股公共行情适配器不能下单：模拟盘由 Trade 撮合，实盘要通过 QMT 执行端。
	ErrNoOrders = errors.New("A 股公共行情适配器只提供行情，不能下单")
)

// ListSource 读完整的 A 股标的列表。
type ListSource func(context.Context) ([]cnstock.Stock, error)

// QuoteSource 取一批股票的实时快照（按标的 ID 索引）。
type QuoteSource func(context.Context, []string) (map[string]cnstock.Quote, error)

// Seed 在标的列表第一次加载完之前，读库里已有的 A 股标的；进程重启后会话不必等标的列表。
type Seed func(context.Context) ([]exchange.Instrument, error)

// Market 是整个进程共用的 A 股公共行情：快照缓存和标的列表各一份，所有 A 股账户共用。
type Market struct {
	Quotes QuoteSource
	List   ListSource
	Seed   Seed
	Now    func() time.Time

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

// NewMarket 用腾讯快照和东方财富列表构造；client 为空时用各自的默认超时。
func NewMarket(client *http.Client, seed Seed) *Market {
	tencent := cnstock.TencentClient{HTTPClient: client}
	return &Market{
		Quotes: tencent.Snapshot,
		List:   func(ctx context.Context) ([]cnstock.Stock, error) { return cnstock.ListAllStocks(ctx, client) },
		Seed:   seed,
	}
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
			log.WarnContextf(ctx, "A 股标的列表刷新失败，沿用已有列表：%v", err)
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
		return errors.New("A 股标的列表来源未配置")
	}
	stocks, err := m.List(ctx)
	if err != nil {
		return err
	}
	at := m.now().UTC()
	instruments := make([]exchange.Instrument, 0, len(stocks))
	for _, stock := range stocks {
		instrument, err := stockInstrument(stock.InstrumentID, at)
		if err != nil {
			return err
		}
		instruments = append(instruments, instrument)
	}
	m.listMu.Lock()
	m.instruments = instruments
	m.listMu.Unlock()
	return nil
}

// stockInstrument 按代码前缀定交易规则：科创板最少 200 股、按 1 股递增，北交所最少 100 股、按 1 股递增，其余 100 股一手；
// 价格步长 0.01 元；价格笼子的比例部分是基准价 ±2%，“10 个最小价位”在市场规则里（exchange.MarketRule.PriceCageTicks）。
// 停牌不写进标的状态（停牌持仓仍要算进组合），只在报价上标记。
func stockInstrument(instrumentID string, at time.Time) (exchange.Instrument, error) {
	board, err := cnstock.BoardOf(instrumentID)
	if err != nil {
		return exchange.Instrument{}, err
	}
	lot := cnstock.LotRuleFor(board)
	return exchange.Instrument{
		Exchange: exchange.ExchangeAShare, MarketType: exchange.MarketTypeStock,
		ExchangeSymbol: instrumentID, InstrumentID: instrumentID,
		BaseAsset: instrumentID, QuoteAsset: SettlementAsset, SettlementAsset: SettlementAsset,
		ExchangeQuantityStep: shared.MustDecimal(fmt.Sprint(lot.Step)),
		MinExchangeQuantity:  shared.MustDecimal(fmt.Sprint(lot.Minimum)),
		PriceTick:            priceTick, MinNotional: shared.Zero(),
		MaxBuyPriceRatio: cageBuyRatio, MinSellPriceRatio: cageSellRatio,
		Status: "TRADING", ExchangeUpdatedAt: at,
	}, nil
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
	return nil, errListPending
}

// PrefetchQuotes 一次取回一批标的的快照（腾讯每次最多 60 只，超过分批），2 秒内取过的不再取；
// 2 秒内取价失败过的代码也不再请求，直接返回那次的错误。
func (m *Market) PrefetchQuotes(ctx context.Context, keys []exchange.SymbolKey) error {
	now := m.now()
	var missing []string
	var recentFailure error
	seen := make(map[string]struct{}, len(keys))
	m.quoteMu.Lock()
	for _, key := range keys {
		if key.Market != exchange.MarketTypeStock {
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
		return errors.New("A 股行情来源未配置")
	}
	fetched, err := m.Quotes(ctx, missing)
	// 缓存时间取请求返回之后：腾讯超时要等 5 秒才返回，用发请求前的时间写进去就已经过期。
	stamp := m.now()
	m.quoteMu.Lock()
	defer m.quoteMu.Unlock()
	if err != nil {
		err = fmt.Errorf("A 股行情不可用：%w", err)
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
	if key.Market != exchange.MarketTypeStock {
		return cachedQuote{}, fmt.Errorf("A 股只有股票市场，不支持 %s", key.Market)
	}
	if err := m.PrefetchQuotes(ctx, []exchange.SymbolKey{key}); err != nil {
		return cachedQuote{}, err
	}
	m.quoteMu.Lock()
	cached, ok := m.quotes[key.Symbol]
	m.quoteMu.Unlock()
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
	return marketQuote(cached)
}

func marketQuote(cached cachedQuote) (execution.MarketQuote, error) {
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
	if result.Last.IsZero() {
		// 当天还没有成交（开盘前）时现价为空，用昨收。
		result.Last = values[3]
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

// Adapter 是一个 A 股账户绑定的公共行情适配器：行情、参考价、标的都来自共用的 Market，不下单、不查私有数据。
type Adapter struct {
	market *Market
}

var (
	_ execution.ExecutionAdapter = (*Adapter)(nil)
	_ execution.QuotePrefetcher  = (*Adapter)(nil)
)

// NewAdapter 返回绑定到共用行情的适配器。
func NewAdapter(market *Market) *Adapter { return &Adapter{market: market} }

func (a *Adapter) Exchange() exchange.Exchange { return exchange.ExchangeAShare }

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

func (a *Adapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	return exchange.AccountSnapshot{}, ErrNoOrders
}

func (a *Adapter) ListPositionSnapshots(context.Context) ([]exchange.Position, error) {
	return nil, ErrNoOrders
}

func (a *Adapter) ListOpenOrders(context.Context) ([]exchange.Order, error) { return nil, ErrNoOrders }

func (a *Adapter) ListRecentFills(context.Context, exchange.SymbolKey, string) ([]exchange.Fill, string, error) {
	return nil, "", ErrNoOrders
}

func (a *Adapter) GetOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, ErrNoOrders
}

func (a *Adapter) GetOrderByExchangeID(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, ErrNoOrders
}

func (a *Adapter) PlaceOrder(context.Context, exchange.OrderRequest) (exchange.Order, error) {
	return exchange.Order{}, ErrNoOrders
}

func (a *Adapter) CancelOrder(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, ErrNoOrders
}
