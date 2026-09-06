package paper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"sort"
	"sync"
	"time"
)

type FactStore interface {
	GetTradingAccountByID(context.Context, string) (store.TradingAccountRecord, error)
	GetPaperBalanceSnapshot(context.Context, string, string) (store.PaperBalanceSnapshot, error)
	GetPaperAccountConfig(context.Context, string, string) (store.PaperAccountConfigRecord, error)
	GetOrderByClientID(context.Context, string, string, string) (store.OrderRecord, error)
	ListOrdersForAccount(context.Context, string, string, int64) ([]store.OrderRecord, error)
	ListFills(context.Context, string, store.FillQuery) ([]store.FillRecord, int64, error)
	ListPositions(context.Context, string, string, string) ([]store.PositionRecord, error)
	ListInstruments(context.Context, string, string) ([]store.InstrumentRecord, error)
}
type Adapter struct {
	Account store.TradingAccountRecord
	Store   FactStore
	// MarketData is the unauthenticated production public adapter. Paper never
	// invokes its private or order methods; it is only used for instruments and
	// reference quotes.
	MarketData  execution.MarketDataSource
	Wake        func()
	Now         func() time.Time
	mu          sync.RWMutex
	instruments map[string]string
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
func PaperIDs(account, client string) (string, string, string) {
	sum := sha256.Sum256([]byte(account + "\x00" + client))
	suffix := hex.EncodeToString(sum[:12])
	return "paper-order-" + suffix, "paper-trade-" + suffix, "paper-fill-" + suffix
}
func (a *Adapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	if a.Store == nil {
		return a.AccountSnapshot(), nil
	}
	account, err := a.Store.GetTradingAccountByID(ctx, a.Account.TradingAccountID)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	projection, err := a.Store.GetPaperBalanceSnapshot(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	balances, locked := projection.Totals, projection.Reserved
	instruments, err := a.Store.ListInstruments(ctx, account.Exchange, account.MarketType)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	settlement := balances[account.SettlementAsset]
	equity := settlement
	usedMargin := shared.Zero()
	unrealizedPnL := shared.Zero()
	if account.MarketType == string(exchange.MarketTypeSpot) {
		byBase := make(map[string]store.InstrumentRecord, len(instruments))
		for _, instrument := range instruments {
			if instrument.QuoteAsset == account.SettlementAsset && instrument.BaseAsset != "" {
				byBase[instrument.BaseAsset] = instrument
			}
		}
		for asset, balance := range balances {
			if asset == account.SettlementAsset || balance.IsZero() {
				continue
			}
			instrument, found := byBase[asset]
			if !found {
				return exchange.AccountSnapshot{}, fmt.Errorf("paper: no valuation instrument for %s", asset)
			}
			quote, quoteErr := a.valuationQuote(ctx, instrument.ExchangeSymbol)
			if quoteErr != nil {
				return exchange.AccountSnapshot{}, quoteErr
			}
			equity = equity.Add(balance.Mul(quote))
		}
	} else {
		positions, positionErr := a.Store.ListPositions(ctx, account.SpaceID, account.TradingAccountID, "")
		if positionErr != nil {
			return exchange.AccountSnapshot{}, positionErr
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
			mark, quoteErr := a.valuationQuote(ctx, position.ExchangeSymbol)
			if quoteErr != nil {
				return exchange.AccountSnapshot{}, quoteErr
			}
			unrealizedPnL = unrealizedPnL.Add(mark.Sub(entry).Mul(quantity))
			usedMargin = usedMargin.Add(quantity.Abs().Mul(mark).Div(leverage))
		}
		equity = settlement.Add(unrealizedPnL)
	}
	available := equity
	if account.MarketType == string(exchange.MarketTypeSwap) {
		available = equity.Sub(usedMargin).Sub(locked[account.SettlementAsset])
	} else {
		available = settlement.Sub(locked[account.SettlementAsset])
	}
	return exchange.AccountSnapshot{
		Balances: paperBalances(balances, locked), Equity: equity, AvailableFunds: available, UsedMargin: usedMargin,
		UnrealizedPnL: unrealizedPnL, ExchangeUpdatedAt: a.now(), Present: exchange.AccountSnapshotPresence{Balances: true, Equity: true, AvailableFunds: true, UsedMargin: true, UnrealizedPnL: true},
	}, nil
}

func (a *Adapter) valuationQuote(ctx context.Context, symbol string) (shared.Decimal, error) {
	quote, err := a.GetQuote(ctx, shared.ExchangeSymbol(symbol))
	if err != nil {
		return shared.Zero(), err
	}
	if !QuoteFresh(quote, a.now(), 10*time.Second) {
		return shared.Zero(), fmt.Errorf("paper: valuation quote is stale for %s", symbol)
	}
	price := quote.Last
	if price.IsZero() {
		price = quote.Ask
	}
	if price.IsZero() {
		price = quote.Bid
	}
	if price.Cmp(shared.Zero()) <= 0 {
		return shared.Zero(), fmt.Errorf("paper: valuation quote is empty for %s", symbol)
	}
	return price, nil
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
			Asset: asset, Available: totalAmount.Sub(lockedAmount), Locked: lockedAmount, Total: totalAmount,
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
	byID := make(map[string]string, len(instruments))
	for _, instrument := range instruments {
		native := instrument.ExchangeSymbol
		if instrument.InstrumentID != "" && native != "" {
			byID[instrument.InstrumentID] = native
		}
	}
	a.mu.Lock()
	a.instruments = byID
	a.mu.Unlock()
	return instruments, nil
}

func (a *Adapter) nativeSymbol(ctx context.Context, symbol string) string {
	a.mu.RLock()
	native, found := a.instruments[symbol]
	a.mu.RUnlock()
	if found {
		return native
	}
	// RPC/operator paths can request a quote before the session's initial
	// snapshot. Lazily populate the same map used by runPaper so canonical
	// instrument IDs never leak into a broker's public endpoint.
	if _, err := a.LoadInstruments(ctx); err == nil {
		a.mu.RLock()
		native, found = a.instruments[symbol]
		a.mu.RUnlock()
		if found {
			return native
		}
	}
	return symbol
}

func (a *Adapter) GetQuote(ctx context.Context, symbol shared.ExchangeSymbol) (execution.MarketQuote, error) {
	if a.MarketData == nil {
		return execution.MarketQuote{}, fmt.Errorf("paper: public market data source is unavailable")
	}
	native := a.nativeSymbol(ctx, symbol.String())
	return a.MarketData.GetQuote(ctx, shared.ExchangeSymbol(native))
}

func (a *Adapter) GetReferencePrice(ctx context.Context, symbol string) (exchange.ReferencePrice, error) {
	quote, err := a.GetQuote(ctx, shared.ExchangeSymbol(symbol))
	if err != nil {
		return exchange.ReferencePrice{}, err
	}
	price := quote.Last
	if price.IsZero() {
		price = quote.Ask
	}
	if price.IsZero() {
		price = quote.Bid
	}
	if price.Cmp(shared.Zero()) <= 0 {
		return exchange.ReferencePrice{}, fmt.Errorf("paper: public reference quote is empty")
	}
	return exchange.ReferencePrice{Price: price, UpdatedAt: quote.SourceTime}, nil
}

func (a *Adapter) referencePrice(ctx context.Context, symbol string) (shared.Decimal, error) {
	quote, err := a.GetReferencePrice(ctx, symbol)
	if err != nil {
		return shared.Zero(), err
	}
	return quote.Price, nil
}
func (a *Adapter) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	if a.Store == nil {
		return nil, nil
	}
	rows, err := a.Store.ListPositions(ctx, a.Account.SpaceID, a.Account.TradingAccountID, "")
	if err != nil {
		return nil, err
	}
	result := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		position := exchange.Position{
			TradingAccountID: row.TradingAccountID, InstrumentID: row.InstrumentID,
			ExchangeSymbol: row.ExchangeSymbol,
			PositionSide:   exchange.PositionSide(row.PositionSide),
			SignedQuantity: decimalOrZero(row.SignedQuantity), EntryPrice: decimalOrZero(row.EntryPrice),
			MarkPrice: decimalOrZero(row.MarkPrice), Leverage: decimalOrZero(row.Leverage),
			MarginMode: exchange.MarginMode(row.MarginMode), UsedMargin: decimalOrZero(row.UsedMargin),
			LiquidationPrice: decimalOrZero(row.LiquidationPrice), UnrealizedPnL: decimalOrZero(row.UnrealizedPnL),
			RealizedPnL: decimalOrZero(row.RealizedPnL), ExchangeUpdatedAt: time.UnixMilli(row.ExchangeUpdatedAt).UTC(),
		}
		if a.Account.MarketType == string(exchange.MarketTypeSwap) && !position.SignedQuantity.IsZero() {
			if quote, quoteErr := a.GetReferencePrice(ctx, position.ExchangeSymbol); quoteErr == nil {
				position.MarkPrice = quote.Price
			}
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
	rows, err := a.Store.ListOrdersForAccount(ctx, a.Account.SpaceID, a.Account.TradingAccountID, 0)
	if err != nil {
		return nil, err
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
func (a *Adapter) ListRecentFills(ctx context.Context, symbol shared.ExchangeSymbol, _ string) ([]exchange.Fill, string, error) {
	if a.Store == nil {
		return nil, "", nil
	}
	rows, _, err := a.Store.ListFills(ctx, a.Account.SpaceID, store.FillQuery{TradingAccountID: a.Account.TradingAccountID, ExchangeSymbol: symbol.String(), Limit: 1000})
	if err != nil {
		return nil, "", err
	}
	result := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		result = append(result, fillFromRecord(row))
	}
	return result, "", nil
}
func (a *Adapter) GetOrder(ctx context.Context, symbol shared.ExchangeSymbol, clientID string) (exchange.Order, error) {
	if a.Store == nil {
		return exchange.Order{}, nil
	}
	row, err := a.Store.GetOrderByClientID(ctx, a.Account.SpaceID, a.Account.TradingAccountID, clientID)
	if err != nil {
		return exchange.Order{}, err
	}
	if symbol != "" && row.ExchangeSymbol != symbol.String() {
		return exchange.Order{}, fmt.Errorf("paper: symbol mismatch")
	}
	result := orderFromRecord(row)
	// Paper cancellation is local and deterministic. Returning the terminal
	// exchange view lets the normal account sync path run ConfirmCancel and
	// release the persisted reservation in the same reducer used by Live.
	if row.State == "CANCELING" || row.State == "CANCEL_UNKNOWN" {
		result.Status = exchange.OrderStatusCanceled
	}
	return result, nil
}
func (a *Adapter) PlaceOrder(_ context.Context, req exchange.OrderRequest) (exchange.Order, error) {
	if a.Wake != nil {
		a.Wake()
	}
	now := a.now()
	symbol := req.ExchangeSymbol
	return exchange.Order{ExchangeOrderID: paperOrderID(a.Account.TradingAccountID, req.ClientOrderID), ClientOrderID: req.ClientOrderID, ExchangeSymbol: symbol, Status: exchange.OrderStatusOpen, CreatedAt: now, UpdatedAt: now}, nil
}
func (a *Adapter) CancelOrder(_ context.Context, symbol shared.ExchangeSymbol, id string) (exchange.Order, error) {
	now := a.now()
	return exchange.Order{ExchangeOrderID: id, ClientOrderID: id, ExchangeSymbol: symbol.String(), Status: exchange.OrderStatusCanceled, CreatedAt: now, UpdatedAt: now}, nil
}

func orderFromRecord(row store.OrderRecord) exchange.Order {
	var limit *shared.Decimal
	if row.LimitPrice != nil {
		value, _ := shared.ParseDecimal(*row.LimitPrice)
		limit = &value
	}
	return exchange.Order{ExchangeOrderID: row.ExchangeOrderID, ClientOrderID: row.ClientOrderID, ExchangeSymbol: row.ExchangeSymbol, OrderType: exchange.OrderType(row.OrderType), TimeInForce: exchange.TimeInForce(row.TimeInForce), Side: exchange.Side(row.Side), PositionSide: exchange.PositionSide(row.PositionSide), Quantity: decimalOrZero(row.Quantity), LimitPrice: limit, FilledQuantity: decimalOrZero(row.FilledQuantity), AveragePrice: decimalOrZero(row.AveragePrice), ReduceOnly: row.ReduceOnly, Status: exchange.OrderStatus(row.State), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func fillFromRecord(row store.FillRecord) exchange.Fill {
	return exchange.Fill{ExchangeTradeID: row.ExchangeTradeID, ExchangeOrderID: row.ExchangeOrderID, ExchangeSymbol: row.ExchangeSymbol, Side: exchange.Side(row.Side), PositionSide: exchange.PositionSide(row.PositionSide), Quantity: decimalOrZero(row.Quantity), Price: decimalOrZero(row.Price), Fee: decimalOrZero(row.Fee), FeeAsset: row.FeeAsset, RealizedPnL: decimalOrZero(row.RealizedPnL), SettlementAsset: row.SettlementAsset, LiquidityRole: row.Role, TradedAt: time.UnixMilli(row.TradedAt).UTC()}
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
func (a *Adapter) SetLeverage(context.Context, shared.ExchangeSymbol, shared.Decimal) error {
	return nil
}
func (a *Adapter) SetMarginMode(context.Context, shared.ExchangeSymbol, exchange.MarginMode) error {
	return nil
}
