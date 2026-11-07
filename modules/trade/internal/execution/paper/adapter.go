package paper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
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
	ListPositions(context.Context, string, string, exchange.SymbolKey) ([]store.PositionRecord, error)
	ListInstruments(context.Context, string, string) ([]store.InstrumentRecord, error)
}

// PublicMarketData 是模拟盘用的公共行情。
type PublicMarketData interface {
	execution.MarketDataSource
	execution.ReferencePriceSource
}

// 编译期确认模拟盘适配器实现了会话按类型断言使用的可选能力。
var (
	_ execution.ExecutionAdapter = (*Adapter)(nil)
	_ execution.SymbolConfigurer = (*Adapter)(nil)
	_ execution.LedgerSource     = (*Adapter)(nil)
)

type Adapter struct {
	Account store.TradingAccountRecord
	Store   FactStore
	// MarketData is the unauthenticated production public adapter. Paper never
	// invokes its private or order methods; it is only used for instruments and
	// reference quotes.
	MarketData  PublicMarketData
	Wake        func()
	Now         func() time.Time
	mu          sync.RWMutex
	instruments map[exchange.SymbolKey]string
	// lastPrices 是现货持仓最近一次取到的估值价（资产 -> 价格），取不到价时用它估值。
	priceMu    sync.Mutex
	lastPrices map[string]shared.Decimal
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
	settlement := balances[account.SettlementAsset]
	equity := settlement
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
		for asset, balance := range balances {
			if asset == account.SettlementAsset || balance.IsZero() {
				continue
			}
			price, priceErr := a.balancePrice(ctx, account, byBase, asset)
			if priceErr != nil {
				return exchange.AccountSnapshot{}, priceErr
			}
			equity = equity.Add(balance.Mul(price))
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
			unrealizedPnL = unrealizedPnL.Add(mark.Sub(entry).Mul(quantity))
			usedMargin = usedMargin.Add(quantity.Abs().Mul(mark).Div(leverage))
		}
		equity = equity.Add(unrealizedPnL)
	}
	// 单一资金池：现货买入和合约保证金都花结算币；现货持有的币不能当保证金。
	available := settlement.Sub(locked[account.SettlementAsset])
	if account.HasPositionMarket() {
		available = settlement.Add(unrealizedPnL).Sub(usedMargin).Sub(locked[account.SettlementAsset])
	}
	paperRows := paperBalances(balances, locked)
	if account.HasPositionMarket() && account.HasBalanceMarket() {
		// 单一资金池里结算币这一行是现货买单看的：扣掉合约占用的保证金和浮亏，但浮盈不能花在现货上（现货不借币），
		// 所以是 现金 + min(浮盈亏, 0) - 保证金 - 冻结。账户级的 AvailableFunds 给合约用，保持含浮盈的口径。
		spotAvailable := settlement.Sub(usedMargin).Sub(locked[account.SettlementAsset])
		if unrealizedPnL.IsNegative() {
			spotAvailable = spotAvailable.Add(unrealizedPnL)
		}
		for i := range paperRows {
			if paperRows[i].Asset == account.SettlementAsset {
				paperRows[i].Available = spotAvailable
			}
		}
	}
	return exchange.AccountSnapshot{
		Balances: paperRows, Equity: equity, AvailableFunds: available, UsedMargin: usedMargin,
		UnrealizedPnL: unrealizedPnL, ExchangeUpdatedAt: a.now(), Present: exchange.AccountSnapshotPresence{Balances: true, Equity: true, AvailableFunds: true, UsedMargin: true, UnrealizedPnL: true},
	}, nil
}

// balancePrice 给现货持有的一个币取估值价，规则与实盘权益采样一致：没有估值标的、标的状态不可交易、
// 交易所确认标的不存在时用最后已知价，没有就按 0 计并记关注项，不让一个下架零头拖垮整个模拟账户的快照；
// 其余取价失败有最后已知价就用，没有照旧报错。
func (a *Adapter) balancePrice(ctx context.Context, account store.TradingAccountRecord, byBase map[string]store.InstrumentRecord, asset string) (shared.Decimal, error) {
	degrade := func() (shared.Decimal, error) {
		last, ok := a.lastPrice(asset)
		how := "按 0 计"
		if ok {
			how = "按最后已知价计"
		}
		telemetry.Note(telemetry.AttentionValuationDegraded, account.Name+"："+asset+" 取不到价，权益"+how, a.now())
		return last, nil
	}
	instrument, found := byBase[asset]
	// 状态为空表示标的列表没有给出状态，照常取价；明确不可交易的不发请求。
	if !found || (instrument.Status != "" && !exchange.StatusTradable(instrument.Status)) {
		return degrade()
	}
	price, err := a.valuationQuote(ctx, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
	if err != nil {
		if _, ok := a.lastPrice(asset); ok || exchange.IsInstrumentNotFound(err) {
			return degrade()
		}
		return shared.Zero(), err
	}
	a.priceMu.Lock()
	if a.lastPrices == nil {
		a.lastPrices = make(map[string]shared.Decimal)
	}
	a.lastPrices[asset] = price
	a.priceMu.Unlock()
	return price, nil
}

func (a *Adapter) lastPrice(asset string) (shared.Decimal, bool) {
	a.priceMu.Lock()
	defer a.priceMu.Unlock()
	price, ok := a.lastPrices[asset]
	if !ok {
		return shared.Zero(), false
	}
	return price, true
}

// valuationQuote 用参考价估值：一笔子单要算多次快照，逐持仓请求盘口会让子单间隔随持仓数变长。
func (a *Adapter) valuationQuote(ctx context.Context, key exchange.SymbolKey) (shared.Decimal, error) {
	quote, err := a.GetReferencePrice(ctx, key)
	if err != nil {
		return shared.Zero(), err
	}
	if !QuoteFresh(execution.MarketQuote{SourceTime: quote.UpdatedAt}, a.now(), 10*time.Second) {
		return shared.Zero(), fmt.Errorf("paper: valuation quote is stale for %s", key)
	}
	if quote.Price.Cmp(shared.Zero()) <= 0 {
		return shared.Zero(), fmt.Errorf("paper: valuation quote is empty for %s", key)
	}
	return quote.Price, nil
}

func paperBalances(total, locked map[string]shared.Decimal) []exchange.AssetBalance {
	assets := make([]string, 0, len(total)+len(locked))
	seen := make(map[string]struct{}, len(total)+len(locked))
	for asset := range total {
		if asset != "" {
			seen[asset] = struct{}{}
			assets = append(assets, asset)
		}
	}
	for asset := range locked {
		if asset != "" {
			if _, found := seen[asset]; !found {
				assets = append(assets, asset)
			}
		}
	}
	sort.Strings(assets)
	result := make([]exchange.AssetBalance, 0, len(assets))
	for _, asset := range assets {
		totalAmount := total[asset]
		lockedAmount := locked[asset]
		result = append(result, exchange.AssetBalance{
			Wallet: exchange.WalletUnified,
			Asset:  asset, Available: totalAmount.Sub(lockedAmount), Locked: lockedAmount, Total: totalAmount,
		})
	}
	return result
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
			position.UnrealizedPnL = position.MarkPrice.Sub(position.EntryPrice).Mul(position.SignedQuantity)
			if position.Leverage.Cmp(shared.Zero()) > 0 {
				position.UsedMargin = position.SignedQuantity.Abs().Mul(position.MarkPrice).Div(position.Leverage)
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
		if row.State == "FILLED" || row.State == "CANCELED" || row.State == "PARTIALLY_CANCELED" || row.State == "REJECTED" || row.State == "EXPIRED" || row.State == "CANCELING" || row.State == "CANCEL_UNKNOWN" {
			continue
		}
		result = append(result, orderFromRecord(row))
	}
	return result, nil
}
func (a *Adapter) ListRecentFills(ctx context.Context, key exchange.SymbolKey, _ string) ([]exchange.Fill, string, error) {
	if a.Store == nil {
		return nil, "", nil
	}
	rows, _, err := a.Store.ListFills(ctx, a.Account.SpaceID, store.FillQuery{TradingAccountID: a.Account.TradingAccountID, Market: string(key.Market), ExchangeSymbol: key.Symbol, Limit: 1000})
	if err != nil {
		return nil, "", storageError(err)
	}
	result := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		result = append(result, fillFromRecord(row))
	}
	return result, "", nil
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
	if a.Wake != nil {
		a.Wake()
	}
	now := a.now()
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

func fillFromRecord(row store.FillRecord) exchange.Fill {
	return exchange.Fill{ExchangeTradeID: row.ExchangeTradeID, ExchangeOrderID: row.ExchangeOrderID, Market: exchange.MarketType(row.MarketType), ExchangeSymbol: row.ExchangeSymbol, Side: exchange.Side(row.Side), PositionSide: exchange.PositionSide(row.PositionSide), Quantity: decimalOrZero(row.Quantity), Price: decimalOrZero(row.Price), Fee: decimalOrZero(row.Fee), FeeAsset: row.FeeAsset, RealizedPnL: decimalOrZero(row.RealizedPnL), SettlementAsset: row.SettlementAsset, LiquidityRole: row.Role, TradedAt: time.UnixMilli(row.TradedAt).UTC()}
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
