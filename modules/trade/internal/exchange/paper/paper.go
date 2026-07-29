package paper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

const fillPageSize = 1000

type Adapter struct {
	base              exchange.Adapter
	store             PaperStore
	spaceID           string
	exchangeAccountID string
	marketType        exchange.MarketType
	settlementAsset   string
	initialBalance    shared.Decimal
	marginMode        exchange.MarginMode
	leverageSettings  store.LeverageSettings

	mu          sync.Mutex
	orders      map[string]exchange.Order
	fills       []exchange.Fill
	instruments map[string]exchange.Instrument
}

type PaperStore interface {
	ListFills(context.Context, string, store.FillQuery) ([]store.FillRecord, int64, error)
	GetOrderByClientID(context.Context, string, string, string) (store.OrderRecord, error)
	ListOrdersForAccount(context.Context, string, string, int64) ([]store.OrderRecord, error)
}

type positionState struct {
	symbol      string
	quantity    shared.Decimal
	entryPrice  shared.Decimal
	markPrice   shared.Decimal
	realizedPnL shared.Decimal
	updatedAt   time.Time
}

func New(
	base exchange.Adapter,
	tradeStore PaperStore,
	spaceID string,
	exchangeAccountID string,
	marketType exchange.MarketType,
	settlementAsset string,
	initialBalance shared.Decimal,
	marginMode exchange.MarginMode,
	leverageSettings store.LeverageSettings,
) *Adapter {
	return &Adapter{
		base: base, store: tradeStore, spaceID: spaceID,
		exchangeAccountID: exchangeAccountID, marketType: marketType,
		settlementAsset: settlementAsset, initialBalance: initialBalance,
		marginMode: marginMode, leverageSettings: cloneSettings(leverageSettings),
		orders:      make(map[string]exchange.Order),
		instruments: make(map[string]exchange.Instrument),
	}
}

func (a *Adapter) Exchange() exchange.Exchange { return a.base.Exchange() }

func (a *Adapter) LoadInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	instruments, err := a.base.LoadInstruments(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, instrument := range instruments {
		a.instruments[instrument.Symbol] = instrument
	}
	return instruments, nil
}

func (a *Adapter) GetReferencePrice(
	ctx context.Context,
	symbol string,
) (exchange.ReferencePrice, error) {
	source, ok := a.base.(exchange.ReferencePriceSource)
	if !ok {
		return exchange.ReferencePrice{}, fmt.Errorf("paper: Exchange has no reference price source")
	}
	return source.GetReferencePrice(ctx, symbol)
}

func (a *Adapter) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	fills, err := a.allFills(ctx)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	now := time.Now().UTC()
	if a.marketType == exchange.MarketTypeSpot {
		balances, err := a.spotBalances(fills)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		availableFunds := balances[a.settlementAsset]
		equity, err := a.spotEquity(ctx, balances)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		return exchange.AccountSnapshot{
			Balances: balanceSlice(balances),
			Equity:   equity, AvailableFunds: availableFunds,
			ExchangeUpdatedAt: now,
			Present:           completeAccountPresence(),
		}, nil
	}

	positions, err := a.swapPositionStates(fills)
	if err != nil {
		return exchange.AccountSnapshot{}, err
	}
	realized := shared.Zero()
	unrealized := shared.Zero()
	usedMargin := shared.Zero()
	for symbol, position := range positions {
		realized = realized.Add(position.realizedPnL)
		if position.quantity.IsZero() {
			continue
		}
		leverage, err := a.leverage(symbol)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		markPrice, err := a.referencePrice(ctx, symbol)
		if err != nil {
			return exchange.AccountSnapshot{}, err
		}
		unrealized = unrealized.Add(
			markPrice.Sub(position.entryPrice).Mul(position.quantity),
		)
		usedMargin = usedMargin.Add(
			position.quantity.Abs().Mul(markPrice).Div(leverage),
		)
	}
	equity := a.initialBalance.Add(realized).Add(unrealized)
	available := equity.Sub(usedMargin)
	return exchange.AccountSnapshot{
		Balances: []exchange.AssetBalance{{
			Asset: a.settlementAsset, Available: available,
			Locked: usedMargin, Total: equity,
		}},
		Equity: equity, AvailableFunds: available, UsedMargin: usedMargin,
		UnrealizedPnL: unrealized, ExchangeUpdatedAt: now,
		Present: completeAccountPresence(),
	}, nil
}

func (a *Adapter) ListPositionSnapshots(ctx context.Context) ([]exchange.Position, error) {
	if a.marketType != exchange.MarketTypeSwap {
		return nil, nil
	}
	fills, err := a.allFills(ctx)
	if err != nil {
		return nil, err
	}
	states, err := a.swapPositionStates(fills)
	if err != nil {
		return nil, err
	}
	symbols := make([]string, 0, len(states))
	for symbol, state := range states {
		if !state.quantity.IsZero() {
			symbols = append(symbols, symbol)
		}
	}
	sort.Strings(symbols)
	positions := make([]exchange.Position, 0, len(symbols))
	for _, symbol := range symbols {
		state := states[symbol]
		leverage, err := a.leverage(symbol)
		if err != nil {
			return nil, err
		}
		markPrice, err := a.referencePrice(ctx, symbol)
		if err != nil {
			return nil, err
		}
		unrealized := markPrice.Sub(state.entryPrice).Mul(state.quantity)
		positions = append(positions, exchange.Position{
			ExchangeAccountID: a.exchangeAccountID,
			Symbol:            symbol, PositionSide: exchange.PositionSideNet,
			SignedQuantity: state.quantity, EntryPrice: state.entryPrice,
			MarkPrice: markPrice, Leverage: leverage,
			MarginMode:    a.marginMode,
			UsedMargin:    state.quantity.Abs().Mul(markPrice).Div(leverage),
			UnrealizedPnL: unrealized, RealizedPnL: state.realizedPnL,
			ExchangeUpdatedAt: state.updatedAt,
			Present: exchange.PositionPresence{
				SignedQuantity: true, EntryPrice: true, MarkPrice: true,
				Leverage: true, MarginMode: true, UsedMargin: true,
				LiquidationPrice: true, UnrealizedPnL: true, RealizedPnL: true,
			},
		})
	}
	return positions, nil
}

func (a *Adapter) ListOpenOrders(context.Context) ([]exchange.Order, error) {
	return nil, nil
}

func (a *Adapter) ListRecentFills(
	ctx context.Context,
	symbol string,
	cursor string,
) ([]exchange.Fill, string, error) {
	fills, err := a.allFills(ctx)
	if err != nil {
		return nil, cursor, err
	}
	offset := 0
	if cursor != "" {
		for index, fill := range fills {
			if fill.Symbol == symbol && fill.ExchangeTradeID == cursor {
				offset = index + 1
				break
			}
		}
	}
	result := make([]exchange.Fill, 0, len(fills)-offset)
	nextCursor := cursor
	for _, fill := range fills[offset:] {
		if fill.Symbol != symbol {
			continue
		}
		result = append(result, fill)
		nextCursor = fill.ExchangeTradeID
	}
	return result, nextCursor, nil
}

func (a *Adapter) GetOrder(
	ctx context.Context,
	_ string,
	clientOrderID string,
) (exchange.Order, error) {
	a.mu.Lock()
	order, ok := a.orders[clientOrderID]
	a.mu.Unlock()
	if ok {
		return order, nil
	}
	if a.store != nil {
		record, err := a.store.GetOrderByClientID(
			ctx, a.spaceID, a.exchangeAccountID, clientOrderID,
		)
		if err == nil && paperSubmitted(record.State) {
			return paperOrder(record)
		}
	}
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound}
}

func (a *Adapter) PlaceOrder(
	ctx context.Context,
	request exchange.OrderRequest,
) (exchange.Order, error) {
	price := request.ReferencePrice
	if request.LimitPrice != nil {
		price = *request.LimitPrice
	}
	if price.Cmp(shared.Zero()) <= 0 {
		return exchange.Order{}, fmt.Errorf("paper: positive execution price is required")
	}
	if err := a.ensureInstrument(ctx, request.Symbol); err != nil {
		return exchange.Order{}, err
	}

	a.mu.Lock()
	if existing, ok := a.orders[request.ClientOrderID]; ok {
		a.mu.Unlock()
		return existing, nil
	}
	a.mu.Unlock()

	realizedPnL := shared.Zero()
	if a.marketType == exchange.MarketTypeSwap {
		fills, err := a.allFills(ctx)
		if err != nil {
			return exchange.Order{}, err
		}
		_, currentTradeID := paperIDs(a.exchangeAccountID, request.ClientOrderID)
		previous := fills[:0]
		for _, fill := range fills {
			if fill.ExchangeTradeID != currentTradeID {
				previous = append(previous, fill)
			}
		}
		states, err := a.swapPositionStates(previous)
		if err != nil {
			return exchange.Order{}, err
		}
		_, realizedPnL = applySwapFill(states[request.Symbol], request.Side, request.Quantity, price)
	}

	now := time.Now().UTC()
	if a.store != nil {
		record, err := a.store.GetOrderByClientID(
			ctx, a.spaceID, a.exchangeAccountID, request.ClientOrderID,
		)
		if err == nil && record.SubmittedAt > 0 {
			now = time.UnixMilli(record.SubmittedAt).UTC()
		}
	}
	exchangeOrderID, exchangeTradeID := paperIDs(
		a.exchangeAccountID,
		request.ClientOrderID,
	)
	order := exchange.Order{
		ExchangeOrderID: exchangeOrderID, ClientOrderID: request.ClientOrderID,
		Symbol: request.Symbol, OrderType: request.OrderType, TimeInForce: request.TimeInForce,
		Side: request.Side, PositionSide: request.PositionSide, Quantity: request.Quantity,
		LimitPrice: request.LimitPrice, FilledQuantity: request.Quantity,
		AveragePrice: price, ReduceOnly: request.ReduceOnly,
		Status: exchange.OrderStatusFilled, CreatedAt: now, UpdatedAt: now,
	}
	fill := exchange.Fill{
		ExchangeTradeID: exchangeTradeID,
		ExchangeOrderID: exchangeOrderID, ClientOrderID: request.ClientOrderID,
		Symbol: request.Symbol, Side: request.Side, PositionSide: request.PositionSide,
		Quantity: request.Quantity, Price: price, Fee: shared.Zero(),
		FeeAsset: a.settlementAsset, SettlementAsset: a.settlementAsset,
		RealizedPnL: realizedPnL, LiquidityRole: "TAKER", TradedAt: now,
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if existing, ok := a.orders[request.ClientOrderID]; ok {
		return existing, nil
	}
	a.orders[request.ClientOrderID] = order
	a.fills = append(a.fills, fill)
	return order, nil
}

func (a *Adapter) CancelOrder(
	ctx context.Context,
	symbol string,
	clientOrderID string,
) (exchange.Order, error) {
	return a.GetOrder(ctx, symbol, clientOrderID)
}

func (*Adapter) SetLeverage(context.Context, string, shared.Decimal) error { return nil }
func (*Adapter) SetMarginMode(context.Context, string, exchange.MarginMode) error {
	return nil
}

func (*Adapter) SubscribePrivate(ctx context.Context, handler exchange.EventHandler) error {
	exchange.NotifyPrivateReady(handler)
	<-ctx.Done()
	return ctx.Err()
}

func (a *Adapter) allFills(ctx context.Context) ([]exchange.Fill, error) {
	byID := make(map[string]exchange.Fill)
	persistedIDs := make(map[string]struct{})
	if a.store != nil {
		for offset := 0; ; offset += fillPageSize {
			records, _, err := a.store.ListFills(ctx, a.spaceID, store.FillQuery{
				ExchangeAccountID: a.exchangeAccountID,
				Offset:            offset, Limit: fillPageSize,
			})
			if err != nil {
				return nil, err
			}
			for _, record := range records {
				fill, err := fillFromRecord(record)
				if err != nil {
					return nil, err
				}
				byID[fill.ExchangeTradeID] = fill
				persistedIDs[fill.ExchangeTradeID] = struct{}{}
			}
			if len(records) < fillPageSize {
				break
			}
		}
		orders, err := a.store.ListOrdersForAccount(
			ctx, a.spaceID, a.exchangeAccountID, 0,
		)
		if err != nil {
			return nil, err
		}
		sort.Slice(orders, func(i, j int) bool {
			if orders[i].SubmittedAt == orders[j].SubmittedAt {
				return orders[i].OrderID < orders[j].OrderID
			}
			return orders[i].SubmittedAt < orders[j].SubmittedAt
		})
		states := make(map[string]positionState)
		for _, record := range orders {
			if !paperSubmitted(record.State) {
				continue
			}
			fill, err := paperFill(record, a.settlementAsset)
			if err != nil {
				return nil, err
			}
			if a.marketType == exchange.MarketTypeSwap {
				next, realized := applySwapFill(
					states[fill.Symbol],
					fill.Side,
					fill.Quantity,
					fill.Price,
				)
				states[fill.Symbol] = next
				fill.RealizedPnL = realized
			}
			if _, persisted := byID[fill.ExchangeTradeID]; !persisted {
				byID[fill.ExchangeTradeID] = fill
			}
		}
	}
	a.mu.Lock()
	for _, fill := range a.fills {
		byID[fill.ExchangeTradeID] = fill
	}
	a.mu.Unlock()
	fills := make([]exchange.Fill, 0, len(byID))
	for _, fill := range byID {
		fills = append(fills, fill)
	}
	sort.Slice(fills, func(i, j int) bool {
		_, leftPersisted := persistedIDs[fills[i].ExchangeTradeID]
		_, rightPersisted := persistedIDs[fills[j].ExchangeTradeID]
		if leftPersisted != rightPersisted {
			return leftPersisted
		}
		if fills[i].TradedAt.Equal(fills[j].TradedAt) {
			return fills[i].ExchangeTradeID < fills[j].ExchangeTradeID
		}
		return fills[i].TradedAt.Before(fills[j].TradedAt)
	})
	return fills, nil
}

func paperSubmitted(state string) bool {
	switch state {
	case "SUBMITTING", "SUBMIT_UNKNOWN", "OPEN", "PARTIALLY_FILLED",
		"CANCELING", "CANCEL_UNKNOWN", "FILLED", "PARTIALLY_CANCELED":
		return true
	default:
		return false
	}
}

func paperOrder(record store.OrderRecord) (exchange.Order, error) {
	quantity, err := shared.ParseDecimal(record.Quantity)
	if err != nil {
		return exchange.Order{}, err
	}
	price, err := paperOrderPrice(record)
	if err != nil {
		return exchange.Order{}, err
	}
	exchangeOrderID, _ := paperIDs(record.ExchangeAccountID, record.ClientOrderID)
	submittedAt := time.UnixMilli(record.SubmittedAt).UTC()
	return exchange.Order{
		ExchangeOrderID: exchangeOrderID, ClientOrderID: record.ClientOrderID,
		Symbol: record.Symbol, OrderType: exchange.OrderType(record.OrderType),
		TimeInForce:  exchange.TimeInForce(record.TimeInForce),
		Side:         exchange.Side(record.Side),
		PositionSide: exchange.PositionSide(record.PositionSide),
		Quantity:     quantity, LimitPrice: decimalPointer(record.LimitPrice),
		FilledQuantity: quantity, AveragePrice: price, ReduceOnly: record.ReduceOnly,
		Status: exchange.OrderStatusFilled, CreatedAt: submittedAt, UpdatedAt: submittedAt,
	}, nil
}

func paperFill(record store.OrderRecord, settlementAsset string) (exchange.Fill, error) {
	order, err := paperOrder(record)
	if err != nil {
		return exchange.Fill{}, err
	}
	_, exchangeTradeID := paperIDs(record.ExchangeAccountID, record.ClientOrderID)
	return exchange.Fill{
		ExchangeTradeID: exchangeTradeID,
		ExchangeOrderID: order.ExchangeOrderID,
		ClientOrderID:   order.ClientOrderID, Symbol: order.Symbol,
		Side: order.Side, PositionSide: order.PositionSide,
		Quantity: order.Quantity, Price: order.AveragePrice, Fee: shared.Zero(),
		FeeAsset: settlementAsset, SettlementAsset: settlementAsset,
		LiquidityRole: "TAKER", TradedAt: order.CreatedAt,
	}, nil
}

func paperOrderPrice(record store.OrderRecord) (shared.Decimal, error) {
	if record.LimitPrice != nil {
		return shared.ParseDecimal(*record.LimitPrice)
	}
	return shared.ParseDecimal(record.ReferencePrice)
}

func decimalPointer(raw *string) *shared.Decimal {
	if raw == nil {
		return nil
	}
	value, err := shared.ParseDecimal(*raw)
	if err != nil {
		return nil
	}
	return &value
}

func paperIDs(exchangeAccountID string, clientOrderID string) (string, string) {
	sum := sha256.Sum256([]byte(exchangeAccountID + "\x00" + clientOrderID))
	suffix := hex.EncodeToString(sum[:12])
	return "paper-order-" + suffix, "paper-fill-" + suffix
}

func (a *Adapter) spotBalances(fills []exchange.Fill) (map[string]shared.Decimal, error) {
	balances := map[string]shared.Decimal{a.settlementAsset: a.initialBalance}
	for _, fill := range fills {
		instrument, err := a.instrument(fill.Symbol)
		if err != nil {
			return nil, err
		}
		notional := fill.Quantity.Mul(fill.Price)
		if fill.Side == exchange.SideBuy {
			balances[instrument.QuoteAsset] = balances[instrument.QuoteAsset].Sub(notional)
			balances[instrument.BaseAsset] = balances[instrument.BaseAsset].Add(fill.Quantity)
		} else {
			balances[instrument.BaseAsset] = balances[instrument.BaseAsset].Sub(fill.Quantity)
			balances[instrument.QuoteAsset] = balances[instrument.QuoteAsset].Add(notional)
		}
		if !fill.Fee.IsZero() {
			balances[fill.FeeAsset] = balances[fill.FeeAsset].Sub(fill.Fee)
		}
	}
	return balances, nil
}

func (a *Adapter) spotEquity(
	ctx context.Context,
	balances map[string]shared.Decimal,
) (shared.Decimal, error) {
	equity := balances[a.settlementAsset]
	a.mu.Lock()
	instruments := make([]exchange.Instrument, 0, len(a.instruments))
	for _, instrument := range a.instruments {
		instruments = append(instruments, instrument)
	}
	a.mu.Unlock()
	sort.Slice(instruments, func(i, j int) bool {
		return instruments[i].Symbol < instruments[j].Symbol
	})
	valuedAssets := make(map[string]struct{})
	for _, instrument := range instruments {
		if instrument.QuoteAsset != a.settlementAsset ||
			instrument.BaseAsset == a.settlementAsset ||
			balances[instrument.BaseAsset].IsZero() {
			continue
		}
		if _, found := valuedAssets[instrument.BaseAsset]; found {
			continue
		}
		price, err := a.referencePrice(ctx, instrument.Symbol)
		if err != nil {
			return shared.Decimal{}, err
		}
		equity = equity.Add(balances[instrument.BaseAsset].Mul(price))
		valuedAssets[instrument.BaseAsset] = struct{}{}
	}
	return equity, nil
}

func (a *Adapter) swapPositionStates(
	fills []exchange.Fill,
) (map[string]positionState, error) {
	states := make(map[string]positionState)
	for _, fill := range fills {
		if _, err := a.instrument(fill.Symbol); err != nil {
			return nil, err
		}
		next, _ := applySwapFill(states[fill.Symbol], fill.Side, fill.Quantity, fill.Price)
		next.symbol = fill.Symbol
		next.updatedAt = fill.TradedAt
		states[fill.Symbol] = next
	}
	return states, nil
}

func applySwapFill(
	current positionState,
	side exchange.Side,
	quantity shared.Decimal,
	price shared.Decimal,
) (positionState, shared.Decimal) {
	delta := quantity
	if side == exchange.SideSell {
		delta = delta.Neg()
	}
	nextQuantity := current.quantity.Add(delta)
	realized := shared.Zero()
	nextEntry := current.entryPrice
	switch {
	case current.quantity.IsZero():
		nextEntry = price
	case current.quantity.IsNegative() == delta.IsNegative():
		nextEntry = current.quantity.Abs().Mul(current.entryPrice).
			Add(delta.Abs().Mul(price)).
			Div(nextQuantity.Abs())
	default:
		closeQuantity := current.quantity.Abs()
		if delta.Abs().Cmp(closeQuantity) < 0 {
			closeQuantity = delta.Abs()
		}
		direction := shared.MustDecimal("1")
		if current.quantity.IsNegative() {
			direction = direction.Neg()
		}
		realized = price.Sub(current.entryPrice).Mul(closeQuantity).Mul(direction)
		if nextQuantity.IsZero() {
			nextEntry = shared.Zero()
		} else if current.quantity.IsNegative() != nextQuantity.IsNegative() {
			nextEntry = price
		}
	}
	current.quantity = nextQuantity
	current.entryPrice = nextEntry
	current.markPrice = price
	current.realizedPnL = current.realizedPnL.Add(realized)
	return current, realized
}

func (a *Adapter) ensureInstrument(ctx context.Context, symbol string) error {
	if _, err := a.instrument(symbol); err == nil {
		return nil
	}
	if _, err := a.LoadInstruments(ctx); err != nil {
		return err
	}
	_, err := a.instrument(symbol)
	return err
}

func (a *Adapter) instrument(symbol string) (exchange.Instrument, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	instrument, found := a.instruments[symbol]
	if !found {
		return exchange.Instrument{}, fmt.Errorf("paper: instrument %s is not loaded", symbol)
	}
	return instrument, nil
}

func (a *Adapter) leverage(symbol string) (shared.Decimal, error) {
	raw := a.leverageSettings[symbol]
	leverage, err := shared.ParseDecimal(raw)
	if err != nil || leverage.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, fmt.Errorf("paper: positive leverage is required for %s", symbol)
	}
	return leverage, nil
}

func (a *Adapter) referencePrice(ctx context.Context, symbol string) (shared.Decimal, error) {
	reference, err := a.GetReferencePrice(ctx, symbol)
	if err != nil {
		return shared.Decimal{}, err
	}
	if reference.Price.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, fmt.Errorf("paper: positive reference price is required for %s", symbol)
	}
	return reference.Price, nil
}

func fillFromRecord(record store.FillRecord) (exchange.Fill, error) {
	price, err := shared.ParseDecimal(record.Price)
	if err != nil {
		return exchange.Fill{}, err
	}
	quantity, err := shared.ParseDecimal(record.Quantity)
	if err != nil {
		return exchange.Fill{}, err
	}
	fee, err := shared.ParseDecimal(record.Fee)
	if err != nil {
		return exchange.Fill{}, err
	}
	realizedPnL, err := shared.ParseDecimal(record.RealizedPnL)
	if err != nil {
		return exchange.Fill{}, err
	}
	return exchange.Fill{
		ExchangeTradeID: record.ExchangeTradeID,
		ExchangeOrderID: record.ExchangeOrderID,
		Symbol:          record.Symbol, Side: exchange.Side(record.Side),
		PositionSide: exchange.PositionSide(record.PositionSide),
		Price:        price, Quantity: quantity, Fee: fee,
		FeeAsset: record.FeeAsset, SettlementAsset: record.SettlementAsset,
		RealizedPnL: realizedPnL, LiquidityRole: record.Role,
		TradedAt: time.UnixMilli(record.TradedAt).UTC(),
	}, nil
}

func balanceSlice(values map[string]shared.Decimal) []exchange.AssetBalance {
	assets := make([]string, 0, len(values))
	for asset := range values {
		assets = append(assets, asset)
	}
	sort.Strings(assets)
	result := make([]exchange.AssetBalance, 0, len(assets))
	for _, asset := range assets {
		result = append(result, exchange.AssetBalance{
			Asset: asset, Available: values[asset], Total: values[asset],
		})
	}
	return result
}

func completeAccountPresence() exchange.AccountSnapshotPresence {
	return exchange.AccountSnapshotPresence{
		Balances: true, Equity: true, AvailableFunds: true,
		UsedMargin: true, MaintenanceMargin: true, UnrealizedPnL: true,
	}
}

func cloneSettings(values store.LeverageSettings) store.LeverageSettings {
	cloned := make(store.LeverageSettings, len(values))
	for symbol, leverage := range values {
		cloned[symbol] = leverage
	}
	return cloned
}
