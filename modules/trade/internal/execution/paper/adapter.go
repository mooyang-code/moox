package paper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"gorm.io/gorm"
)

type FactStore interface {
	GetTradingAccountByID(context.Context, string) (store.TradingAccountRecord, error)
	GetPaperBalanceSnapshot(context.Context, string, string) (store.PaperBalanceSnapshot, error)
	GetPaperAccountConfig(context.Context, string, string) (store.PaperAccountConfigRecord, error)
	GetOrderByClientID(context.Context, string, string, exchange.MarketType, string) (store.OrderRecord, error)
	ListActiveOrdersForAccount(context.Context, string, string) ([]store.OrderRecord, error)
	GetOrderByExchangeID(context.Context, string, string, exchange.SymbolKey, string) (store.OrderRecord, error)
	ListFills(context.Context, string, store.FillQuery) ([]store.FillRecord, int64, error)
	GetInstrumentSummary(context.Context, string, string, exchange.SymbolKey) (store.InstrumentSummaryRecord, bool, error)
	ListPositions(context.Context, string, string, exchange.SymbolKey) ([]store.PositionRecord, error)
	ListInstruments(context.Context, string, string) ([]store.InstrumentRecord, error)
}

// PublicMarketData 是模拟盘用的公共行情。能查资金费率（execution.FundingRatesOf）时，永续持仓按它结算资金费。
type PublicMarketData interface {
	execution.MarketDataSource
	execution.ReferencePriceSource
}

// 编译期确认模拟盘适配器实现了会话按类型断言使用的可选能力与订单策略边界。
var (
	_ execution.ExecutionAdapter = (*Adapter)(nil)
	_ execution.SymbolConfigurer = (*Adapter)(nil)
	_ execution.LedgerSource     = (*Adapter)(nil)
	_ execution.QuotePrefetcher  = (*Adapter)(nil)
	_ execution.ExchangeIdentity = (*Adapter)(nil)
	_ execution.OrderPolicy      = (*Adapter)(nil)
)

func (a *Adapter) orderPolicy() execution.PaperOrderPolicy {
	return execution.PaperOrderPolicy{Adapter: a}
}

func (a *Adapter) NeedsExecutableQuote() bool { return a.orderPolicy().NeedsExecutableQuote() }
func (a *Adapter) ResolveExecutableQuote(ctx context.Context, key exchange.SymbolKey, side exchange.Side, now time.Time, maxAge time.Duration) (shared.Decimal, time.Time, error) {
	return a.orderPolicy().ResolveExecutableQuote(ctx, key, side, now, maxAge)
}
func (a *Adapter) UsesLocalFundsLedger() bool  { return a.orderPolicy().UsesLocalFundsLedger() }
func (a *Adapter) ConfirmsCancelLocally() bool { return a.orderPolicy().ConfirmsCancelLocally() }
func (a *Adapter) NeedsMarginPoolSnapshot(account tradingaccount.Account, reservedAsset string, reduceOnly bool) bool {
	return a.orderPolicy().NeedsMarginPoolSnapshot(account, reservedAsset, reduceOnly)
}
func (a *Adapter) MarginPoolAdjustment(market exchange.MarketType, usedMargin, unrealizedPnL shared.Decimal) shared.Decimal {
	return a.orderPolicy().MarginPoolAdjustment(market, usedMargin, unrealizedPnL)
}
func (a *Adapter) ComputeReservation(req execution.ReservationRequest) (execution.ReservationResult, error) {
	return a.orderPolicy().ComputeReservation(req)
}
func (a *Adapter) RepriceMarketOrder(req execution.RepriceRequest) (shared.Decimal, shared.Decimal, error) {
	return a.orderPolicy().RepriceMarketOrder(req)
}
func (a *Adapter) ClampBuyCapacity(req execution.CapacityClampRequest) shared.Decimal {
	return a.orderPolicy().ClampBuyCapacity(req)
}
func (a *Adapter) DefaultLeverage() (shared.Decimal, bool) {
	return a.orderPolicy().DefaultLeverage()
}

// Exchange 返回模拟账户所在的场所，报价新鲜度按这个场所的交易时段判断。
func (a *Adapter) Exchange() exchange.Exchange { return exchange.Exchange(a.Account.Exchange) }

// PrefetchQuotes 交给公共行情批量预取（A 股腾讯快照一次 60 只）；公共行情不支持时什么都不做。
func (a *Adapter) PrefetchQuotes(ctx context.Context, keys []exchange.SymbolKey) error {
	if prefetcher, ok := a.MarketData.(execution.QuotePrefetcher); ok {
		return prefetcher.PrefetchQuotes(ctx, keys)
	}
	return nil
}

// lastPriceFresh 判断降级用的最后已知价还能不能用：A 股时段外用收盘价（execution.QuoteFresh）；
// 没有交易时段的场所保持原来的判断，取价时间前后 maxAge 以内。
func (a *Adapter) lastPriceFresh(maxAge time.Duration) func(time.Time) bool {
	return func(at time.Time) bool {
		if execution.HasSession(a.Exchanges, a.Account.Exchange) {
			return execution.QuoteFresh(a.Exchanges, a.Account.Exchange, at, a.now(), maxAge)
		}
		age := a.now().Sub(at)
		return age <= maxAge && age >= -maxAge
	}
}

type Adapter struct {
	Account store.TradingAccountRecord
	Store   FactStore
	// MarketData is the unauthenticated production public adapter. Paper never
	// invokes its private or order methods; it is only used for instruments and
	// reference quotes.
	MarketData PublicMarketData
	// Exchanges 提供场所的交易时段：A 股时段外估值报价按收盘价判断新鲜度；为空时全天按时段内处理。
	Exchanges   execution.ExchangeCatalog
	Wake        func()
	Now         func() time.Time
	mu          sync.RWMutex
	instruments map[exchange.SymbolKey]string
	// lastPrices 是现货持仓最近一次取到的估值价和取价时间（资产 -> 价格），取不到价时在保留时间内用它估值。
	priceMu    sync.Mutex
	lastPrices map[string]execution.LastPrice
	// funding 是资金费结算的内存进度。
	funding fundingProgress
}

// blockedSetSource 是能读下架名单的事实存储（*store.Store 实现）；测试替身没有实现时当作名单为空。
type blockedSetSource interface {
	LoadBlockedSet(context.Context) (store.BlockedSet, error)
}

func (a *Adapter) now() time.Time {
	if a != nil && a.Now != nil {
		return a.Now()
	}
	return time.Now().UTC()
}
func paperOrderID(account, client string) string {
	sum := sha256.Sum256([]byte(account + "\x00" + client))
	return "paper-order-" + hex.EncodeToString(sum[:12])
}
func (a *Adapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	if a.Store == nil {
		return a.AccountSnapshot(), nil
	}
	account, err := a.Store.GetTradingAccountByID(ctx, a.Account.TradingAccountID)
	if err != nil {
		return exchange.AccountSnapshot{}, storageError(err)
	}
	projection, err := a.Store.GetPaperBalanceSnapshot(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return exchange.AccountSnapshot{}, storageError(err)
	}
	balances, locked := projection.Totals, projection.Reserved
	assetPrices := make(map[string]shared.Decimal)
	usedMargin := shared.Zero()
	unrealizedPnL := shared.Zero()
	if account.HasBalanceMarket() {
		// 现货的非结算币按基础资产找交易对估值；合约按持仓估值，不读全部标的（每笔撮合要算两次快照）。
		byBase := make(map[string]store.InstrumentRecord)
		for _, market := range account.Markets {
			if !exchange.RuleFor(market).PositionFromBalance {
				continue
			}
			instruments, err := a.Store.ListInstruments(ctx, account.Exchange, market)
			if err != nil {
				return exchange.AccountSnapshot{}, storageError(err)
			}
			for _, instrument := range instruments {
				if instrument.QuoteAsset == account.SettlementAsset && instrument.BaseAsset != "" {
					byBase[instrument.BaseAsset] = instrument
				}
			}
		}
		// 持有的标的先批量预取报价（A 股腾讯快照一次 60 只），之后逐个估值命中缓存。
		keys := make([]exchange.SymbolKey, 0, len(balances))
		for asset, balance := range balances {
			if instrument, found := byBase[asset]; found && asset != account.SettlementAsset && !balance.IsZero() {
				keys = append(keys, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
			}
		}
		execution.PrefetchQuotes(ctx, a, keys)
		for asset, balance := range balances {
			if asset == account.SettlementAsset || balance.IsZero() {
				continue
			}
			price, priceErr := a.balancePrice(ctx, account, byBase, asset)
			if priceErr != nil {
				return exchange.AccountSnapshot{}, priceErr
			}
			assetPrices[asset] = price
		}
	}
	if account.HasPositionMarket() {
		positions, positionErr := a.Store.ListPositions(ctx, account.SpaceID, account.TradingAccountID, exchange.SymbolKey{})
		if positionErr != nil {
			return exchange.AccountSnapshot{}, storageError(positionErr)
		}
		for _, position := range positions {
			quantity, parseErr := shared.ParseDecimal(position.SignedQuantity)
			if parseErr != nil {
				return exchange.AccountSnapshot{}, fmt.Errorf("%w: paper position quantity", store.ErrInvalidRecord)
			}
			if quantity.IsZero() {
				continue
			}
			entry, parseErr := shared.ParseDecimal(position.EntryPrice)
			if parseErr != nil || entry.Cmp(shared.Zero()) <= 0 {
				return exchange.AccountSnapshot{}, fmt.Errorf("%w: paper position entry price", store.ErrInvalidRecord)
			}
			leverage, parseErr := shared.ParseDecimal(position.Leverage)
			if parseErr != nil || leverage.Cmp(shared.Zero()) <= 0 {
				return exchange.AccountSnapshot{}, fmt.Errorf("%w: paper position leverage", store.ErrInvalidRecord)
			}
			mark, quoteErr := a.valuationQuote(ctx, exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol))
			if quoteErr != nil {
				return exchange.AccountSnapshot{}, quoteErr
			}
			posMargin, posPnL := tradingaccount.PaperPositionValuation(quantity, entry, mark, leverage)
			usedMargin = usedMargin.Add(posMargin)
			unrealizedPnL = unrealizedPnL.Add(posPnL)
		}
	}
	t1Locked, err := a.loadT1Locked(ctx, account)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	computed := tradingaccount.ComputePaperEquity(tradingaccount.PaperEquityInput{
		SettlementAsset:   account.SettlementAsset,
		HasBalanceMarket:  account.HasBalanceMarket(),
		HasPositionMarket: account.HasPositionMarket(),
		Totals:            balances,
		Locked:            locked,
		T1Locked:          t1Locked,
		AssetPrices:       assetPrices,
		UsedMargin:        usedMargin,
		UnrealizedPnL:     unrealizedPnL,
	})
	return exchange.AccountSnapshot{
		Balances:          computed.Balances,
		Equity:            computed.Equity,
		AvailableFunds:    computed.AvailableFunds,
		UsedMargin:        computed.UsedMargin,
		UnrealizedPnL:     computed.UnrealizedPnL,
		ExchangeUpdatedAt: a.now(),
		Present:           exchange.AccountSnapshotPresence{Balances: true, Equity: true, AvailableFunds: true, UsedMargin: true, UnrealizedPnL: true},
	}, nil
}

// loadT1Locked 读当日买入（北京时间当天）不可卖数量：T+1 的场所（A 股）才查，港股与加密货币返回 nil。
func (a *Adapter) loadT1Locked(ctx context.Context, account store.TradingAccountRecord) (map[string]shared.Decimal, error) {
	if !slices.ContainsFunc(account.Markets, func(market string) bool { return exchange.RuleAt(account.Exchange, market).T1 }) {
		return nil, nil
	}
	source, ok := a.Store.(t1Source)
	if !ok {
		return nil, nil
	}
	bought, err := source.PaperT1Bought(ctx, account.SpaceID, account.TradingAccountID, exchange.T1Since(a.now()).UnixMilli())
	if err != nil {
		return nil, storageError(err)
	}
	return bought, nil
}

// t1Source 是能读当日买入数量的事实存储（*store.Store 实现）；测试替身没有实现时不扣 T+1。
type t1Source interface {
	PaperT1Bought(ctx context.Context, spaceID, accountID string, since int64) (map[string]shared.Decimal, error)
}

// balancePrice 给现货持有的一个币取估值价，取不到新鲜报价时和实盘权益采样用同一套降级规则（execution.DegradedPrice）：
// 不超龄的最后已知价；没有估值标的、交易所确认标的不存在、不在交易状态又在下架名单里的按 0 计；其余不在交易状态的按本账户最近一笔成交价计；
// 标的仍在交易只是暂时取价失败的照旧报错（在下架名单里也一样）。用上降级估值时记关注项。
func (a *Adapter) balancePrice(ctx context.Context, account store.TradingAccountRecord, byBase map[string]store.InstrumentRecord, asset string) (shared.Decimal, error) {
	instrument, found := byBase[asset]
	if !found {
		return a.degradedPrice(ctx, account, asset, nil, execution.ValuationMissingInstrument, fmt.Errorf("paper: 没有 %s/%s 现货标的", asset, account.SettlementAsset))
	}
	// 状态为空表示标的列表没有给出状态，照常取价；明确不可交易的不发请求。
	if instrument.Status != "" && !exchange.StatusTradable(instrument.Status) {
		return a.degradedPrice(ctx, account, asset, &instrument, execution.ValuationNotTrading,
			fmt.Errorf("paper: 标的 %s 状态为 %s", instrument.ExchangeSymbol, instrument.Status))
	}
	price, err := a.valuationQuote(ctx, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
	if err != nil {
		failure := execution.ValuationQuoteFailed
		if exchange.IsInstrumentNotFound(err) {
			failure = execution.ValuationInstrumentGone
		}
		return a.degradedPrice(ctx, account, asset, &instrument, failure, err)
	}
	a.priceMu.Lock()
	if a.lastPrices == nil {
		a.lastPrices = make(map[string]execution.LastPrice)
	}
	a.lastPrices[asset] = execution.LastPrice{Price: price, At: a.now()}
	a.priceMu.Unlock()
	return price, nil
}

func (a *Adapter) degradedPrice(
	ctx context.Context,
	account store.TradingAccountRecord,
	asset string,
	instrument *store.InstrumentRecord,
	failure execution.ValuationFailure,
	cause error,
) (shared.Decimal, error) {
	// 只有已经不可交易的标的才因为在下架名单里按 0 计；仍在交易的照旧取价失败报错。
	if instrument != nil && failure == execution.ValuationNotTrading {
		if source, ok := a.Store.(blockedSetSource); ok {
			blocked, err := source.LoadBlockedSet(ctx)
			if err != nil {
				return shared.Zero(), storageError(err)
			}
			if blocked.ContainsInstrument(account, *instrument) {
				failure = execution.ValuationDelisted
			}
		}
	}
	var lastFill execution.LastFillPrice
	if instrument != nil && a.Store != nil {
		// 最近成交价取标的汇总里记的：订单与成交按保留期清理之后仍在。
		lastFill = func(ctx context.Context) (shared.Decimal, bool, error) {
			summary, found, err := a.Store.GetInstrumentSummary(ctx, account.SpaceID, account.TradingAccountID,
				exchange.NewSymbolKey(exchange.MarketType(instrument.MarketType), instrument.ExchangeSymbol))
			if err != nil {
				return shared.Zero(), false, storageError(err)
			}
			return summary.LastPrice, found && !summary.LastPrice.IsZero(), nil
		}
	}
	value, err := execution.DegradedPrice(ctx, failure, a.lastPrice(asset), a.lastPriceFresh(execution.LastPriceMaxAge), lastFill, cause)
	if err != nil {
		return shared.Zero(), err
	}
	telemetry.Note(telemetry.AttentionValuationDegraded, account.Name+"："+asset+" 取不到价，权益"+value.How, a.now())
	return value.Price, nil
}

func (a *Adapter) lastPrice(asset string) execution.LastPrice {
	a.priceMu.Lock()
	defer a.priceMu.Unlock()
	return a.lastPrices[asset]
}

// valuationQuoteMaxAge 是时段内估值报价的最长时延。
const valuationQuoteMaxAge = 10 * time.Second

// valuationQuote 用参考价估值：一笔子单要算多次快照，逐持仓请求盘口会让子单间隔随持仓数变长。
func (a *Adapter) valuationQuote(ctx context.Context, key exchange.SymbolKey) (shared.Decimal, error) {
	quote, err := a.GetReferencePrice(ctx, key)
	if err != nil {
		return shared.Zero(), err
	}
	if !strictQuoteFresh(a.Exchanges, a.Account.Exchange, quote.UpdatedAt, a.now(), valuationQuoteMaxAge) {
		return shared.Zero(), fmt.Errorf("paper: valuation quote is stale for %s", key)
	}
	if quote.Price.Cmp(shared.Zero()) <= 0 {
		return shared.Zero(), fmt.Errorf("paper: valuation quote is empty for %s", key)
	}
	return quote.Price, nil
}

func (a *Adapter) AccountSnapshot() exchange.AccountSnapshot {
	return exchange.AccountSnapshot{ExchangeUpdatedAt: a.now()}
}
func (a *Adapter) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	if a.MarketData == nil {
		return nil, nil
	}
	instruments, err := a.MarketData.LoadInstruments(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[exchange.SymbolKey]string, len(instruments))
	for _, instrument := range instruments {
		native := instrument.ExchangeSymbol
		if native != "" {
			byID[exchange.NewSymbolKey(instrument.MarketType, native)] = native
		}
		if instrument.InstrumentID != "" && native != "" {
			byID[exchange.NewSymbolKey(instrument.MarketType, instrument.InstrumentID)] = native
		}
	}
	a.mu.Lock()
	a.instruments = byID
	a.mu.Unlock()
	return instruments, nil
}

// nativeSymbol 把内部标的 ID 或交易所代码换成交易所代码；同名代码按市场区分。
func (a *Adapter) nativeSymbol(ctx context.Context, key exchange.SymbolKey) (string, error) {
	a.mu.RLock()
	native, found := a.instruments[key]
	a.mu.RUnlock()
	if found {
		return native, nil
	}
	// RPC/operator paths can request a quote before the session's initial
	// snapshot. Lazily populate the same map used by runPaper so canonical
	// instrument IDs never leak into an exchange's public endpoint.
	if _, err := a.LoadInstruments(ctx); err != nil {
		return "", err
	}
	a.mu.RLock()
	native, found = a.instruments[key]
	a.mu.RUnlock()
	if found {
		return native, nil
	}
	return key.Symbol, nil
}

func (a *Adapter) GetQuote(ctx context.Context, key exchange.SymbolKey) (execution.MarketQuote, error) {
	if a.MarketData == nil {
		return execution.MarketQuote{}, fmt.Errorf("paper: public market data source is unavailable")
	}
	native, err := a.nativeSymbol(ctx, key)
	if err != nil {
		return execution.MarketQuote{}, err
	}
	return a.MarketData.GetQuote(ctx, exchange.SymbolKey{Market: key.Market, Symbol: native})
}

// GetReferencePrice 用公共行情的参考价：目标换算要一次取几百个标的，不能逐个请求盘口。
func (a *Adapter) GetReferencePrice(ctx context.Context, key exchange.SymbolKey) (exchange.ReferencePrice, error) {
	if a.MarketData == nil {
		return exchange.ReferencePrice{}, errors.New("paper: public market data is not configured")
	}
	native, err := a.nativeSymbol(ctx, key)
	if err != nil {
		return exchange.ReferencePrice{}, err
	}
	return a.MarketData.GetReferencePrice(ctx, exchange.SymbolKey{Market: key.Market, Symbol: native})
}

func (a *Adapter) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	if a.Store == nil {
		return nil, nil
	}
	rows, err := a.Store.ListPositions(ctx, a.Account.SpaceID, a.Account.TradingAccountID, exchange.SymbolKey{})
	if err != nil {
		return nil, storageError(err)
	}
	result := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		position := exchange.Position{
			TradingAccountID: row.TradingAccountID, Market: exchange.MarketType(row.MarketType), InstrumentID: row.InstrumentID,
			ExchangeSymbol: row.ExchangeSymbol,
			PositionSide:   exchange.PositionSide(row.PositionSide),
			SignedQuantity: decimalOrZero(row.SignedQuantity), EntryPrice: decimalOrZero(row.EntryPrice),
			MarkPrice: decimalOrZero(row.MarkPrice), Leverage: decimalOrZero(row.Leverage),
			MarginMode: exchange.MarginMode(row.MarginMode), UsedMargin: decimalOrZero(row.UsedMargin),
			LiquidationPrice: decimalOrZero(row.LiquidationPrice), UnrealizedPnL: decimalOrZero(row.UnrealizedPnL),
			RealizedPnL: decimalOrZero(row.RealizedPnL), ExchangeUpdatedAt: time.UnixMilli(row.ExchangeUpdatedAt).UTC(),
		}
		if position.Market == exchange.MarketTypeSwap && !position.SignedQuantity.IsZero() {
			quote, quoteErr := a.GetReferencePrice(ctx, exchange.NewSymbolKey(position.Market, position.ExchangeSymbol))
			if quoteErr != nil {
				return nil, quoteErr
			}
			position.MarkPrice = quote.Price
			usedMargin, unrealizedPnL := tradingaccount.PaperPositionValuation(position.SignedQuantity, position.EntryPrice, position.MarkPrice, position.Leverage)
			position.UnrealizedPnL = unrealizedPnL
			if position.Leverage.Cmp(shared.Zero()) > 0 {
				position.UsedMargin = usedMargin
			}
		}
		result = append(result, position)
	}
	return result, nil
}
func (a *Adapter) ListOpenOrders(ctx context.Context) ([]exchange.Order, error) {
	if a.Store == nil {
		return nil, nil
	}
	rows, err := a.Store.ListActiveOrdersForAccount(ctx, a.Account.SpaceID, a.Account.TradingAccountID)
	if err != nil {
		return nil, storageError(err)
	}
	result := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		// 查询结果已只含未结束订单；撤单中的订单不算挂单。
		if row.State == "CANCELING" || row.State == "CANCEL_UNKNOWN" {
			continue
		}
		result = append(result, orderFromRecord(row))
	}
	return result, nil
}

// ListRecentFills 对模拟盘恒为空：模拟盘成交是撮合器在同一事务里和余额、持仓一起写进库的，
// 同步时再把库里的成交读出来逐笔入账只会全部判为重复、白白持锁，没有任何作用。
func (a *Adapter) ListRecentFills(context.Context, exchange.SymbolKey, string) ([]exchange.Fill, string, error) {
	return nil, "", nil
}
func (a *Adapter) GetOrder(ctx context.Context, key exchange.SymbolKey, clientID string) (exchange.Order, error) {
	if a.Store == nil {
		return exchange.Order{}, nil
	}
	row, err := a.Store.GetOrderByClientID(ctx, a.Account.SpaceID, a.Account.TradingAccountID, key.Market, clientID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound, Err: err}
		}
		return exchange.Order{}, storageError(err)
	}
	if key.Symbol != "" && (row.ExchangeSymbol != key.Symbol || (key.Market != "" && row.MarketType != string(key.Market))) {
		return exchange.Order{}, fmt.Errorf("paper: symbol mismatch")
	}
	result := orderFromRecord(row)
	// A crash can happen after the local Paper matcher accepted PlaceOrder but
	// before the execution result was persisted. Paper order IDs are derived
	// solely from account and client ID, so reconstruct the deterministic
	// exchange identity instead of leaving SubmitUnknown permanently stuck.
	if result.ExchangeOrderID == "" && (row.State == "SUBMITTING" || row.State == "SUBMIT_UNKNOWN") {
		result.ExchangeOrderID = paperOrderID(a.Account.TradingAccountID, row.ClientOrderID)
		result.Status = exchange.OrderStatusOpen
	}
	// Paper cancellation is local and deterministic. Returning the terminal
	// exchange view lets the normal account sync path run ConfirmCancel and
	// release the persisted reservation in the same reducer used by Live.
	if row.State == "CANCELING" || row.State == "CANCEL_UNKNOWN" {
		result.Status = exchange.OrderStatusCanceled
	}
	return result, nil
}
func (a *Adapter) GetOrderByExchangeID(ctx context.Context, key exchange.SymbolKey, exchangeOrderID string) (exchange.Order, error) {
	if a.Store == nil {
		return exchange.Order{}, nil
	}
	row, err := a.Store.GetOrderByExchangeID(ctx, a.Account.SpaceID, a.Account.TradingAccountID, key, exchangeOrderID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound, Err: err}
		}
		return exchange.Order{}, storageError(err)
	}
	return orderFromRecord(row), nil
}

func (a *Adapter) PlaceOrder(_ context.Context, req exchange.OrderRequest) (exchange.Order, error) {
	now := a.now()
	// 交易时段拒单收口在 ExecutionAdapter 边界：有交易时段的场所休市时直接拒绝受理新单。
	if a.Exchanges != nil && !execution.InSession(a.Exchanges, a.Account.Exchange, now) {
		return exchange.Order{}, &exchange.Error{
			Kind: exchange.ErrorRejected,
			Err:  execution.ErrOutOfSession,
		}
	}
	if a.Wake != nil {
		a.Wake()
	}
	symbol := req.ExchangeSymbol
	return exchange.Order{ExchangeOrderID: paperOrderID(a.Account.TradingAccountID, req.ClientOrderID), ClientOrderID: req.ClientOrderID, Market: req.Market, ExchangeSymbol: symbol, Status: exchange.OrderStatusOpen, CreatedAt: now, UpdatedAt: now}, nil
}
func (a *Adapter) CancelOrder(_ context.Context, key exchange.SymbolKey, id string) (exchange.Order, error) {
	now := a.now()
	return exchange.Order{ExchangeOrderID: id, ClientOrderID: id, Market: key.Market, ExchangeSymbol: key.Symbol, Status: exchange.OrderStatusCanceled, CreatedAt: now, UpdatedAt: now}, nil
}

func orderFromRecord(row store.OrderRecord) exchange.Order {
	var limit *shared.Decimal
	if row.LimitPrice != nil {
		value, _ := shared.ParseDecimal(*row.LimitPrice)
		limit = &value
	}
	return exchange.Order{ExchangeOrderID: row.ExchangeOrderID, ClientOrderID: row.ClientOrderID, Market: exchange.MarketType(row.MarketType), ExchangeSymbol: row.ExchangeSymbol, OrderType: exchange.OrderType(row.OrderType), TimeInForce: exchange.TimeInForce(row.TimeInForce), Side: exchange.Side(row.Side), PositionSide: exchange.PositionSide(row.PositionSide), Quantity: decimalOrZero(row.Quantity), LimitPrice: limit, FilledQuantity: decimalOrZero(row.FilledQuantity), AveragePrice: decimalOrZero(row.AveragePrice), ReduceOnly: row.ReduceOnly, Status: exchange.OrderStatus(row.State), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func decimalOrZero(raw string) shared.Decimal {
	if raw == "" {
		return shared.Zero()
	}
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return value
}
func (a *Adapter) SetLeverage(context.Context, exchange.SymbolKey, shared.Decimal) error {
	return nil
}
func (a *Adapter) SetMarginMode(context.Context, exchange.SymbolKey, exchange.MarginMode) error {
	return nil
}
