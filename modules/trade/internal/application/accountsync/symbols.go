package accountsync

import (
	"context"
	"sort"
	"strings"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// rotationBatches 是轮转批次数：每轮多拉全集的十分之一，30 秒一轮时 5 分钟走完一圈。
const rotationBatches = 10

// FullSymbols 是账户可能有成交的全部标的：有成交游标、有持仓、有挂单或近期订单的标的，
// 以及现货持有资产对应的交易对。会话启动和手动同步拉这个全集。
func FullSymbols(
	account store.TradingAccountRecord,
	openOrders []exchange.Order,
	localOrders []store.OrderRecord,
	positions []exchange.Position,
	instruments []store.InstrumentRecord,
	snapshot exchange.AccountSnapshot,
) []string {
	set := make(map[string]struct{})
	add := func(symbol string) {
		if symbol != "" {
			set[symbol] = struct{}{}
		}
	}
	for symbol := range account.FillCursors {
		add(symbol)
	}
	for _, current := range openOrders {
		add(current.ExchangeSymbol)
	}
	for _, current := range localOrders {
		add(current.ExchangeSymbol)
	}
	for _, position := range positions {
		add(position.ExchangeSymbol)
	}
	if exchange.RuleFor(account.MarketType).PositionFromBalance {
		held := make(map[string]struct{})
		for _, balance := range snapshot.Balances {
			if balance.Total.Cmp(shared.Zero()) > 0 ||
				balance.Available.Cmp(shared.Zero()) > 0 ||
				balance.Locked.Cmp(shared.Zero()) > 0 {
				held[balance.Asset] = struct{}{}
			}
		}
		for _, symbol := range symbolsForAssets(account, instruments, held) {
			add(symbol)
		}
	}
	return sortedSymbols(set)
}

// fullSymbolCap 是一次完整同步最多拉成交的标的数：491 个标的全拉要 2455 权重，超过永续 1 分钟额度。
// 必须拉的标的（订单、成交缺口、持仓变化）不受限制，其余由轮转在后续对账中补齐。
const fullSymbolCap = 60

// BoundedFullSymbols 用于会话启动和手动同步：先放必须拉的标的，再从全集里补到上限。
func (s *Service) BoundedFullSymbols(
	ctx context.Context,
	account store.TradingAccountRecord,
	openOrders []exchange.Order,
	localOrders []store.OrderRecord,
	positions []exchange.Position,
	instruments []store.InstrumentRecord,
	snapshot exchange.AccountSnapshot,
) ([]string, error) {
	full := FullSymbols(account, openOrders, localOrders, positions, instruments, snapshot)
	set := make(map[string]struct{})
	for _, current := range openOrders {
		set[current.ExchangeSymbol] = struct{}{}
	}
	for _, current := range localOrders {
		set[current.ExchangeSymbol] = struct{}{}
	}
	gaps, err := s.Store.OrderSymbolsWithFillGap(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	for _, symbol := range gaps {
		set[symbol] = struct{}{}
	}
	changed, err := s.changedPositionSymbols(ctx, account, positions)
	if err != nil {
		return nil, err
	}
	for _, symbol := range changed {
		set[symbol] = struct{}{}
	}
	if exchange.RuleFor(account.MarketType).PositionFromBalance {
		for _, symbol := range symbolsForAssets(account, instruments, changedAssets(account.Snapshot.Balances, snapshot.Balances)) {
			set[symbol] = struct{}{}
		}
	}
	delete(set, "")
	for _, symbol := range s.nextRotationBatch(account.TradingAccountID, full) {
		if len(set) >= fullSymbolCap {
			break
		}
		set[symbol] = struct{}{}
	}
	return sortedSymbols(set), nil
}

// incrementalSymbols 只选本轮需要拉成交的标的：活动订单、成交缺口、持仓或余额变化的标的，
// 再加一批轮转的标的，用来发现没有其他信号的外部成交。
func (s *Service) incrementalSymbols(
	ctx context.Context,
	account store.TradingAccountRecord,
	openOrders []exchange.Order,
	localOrders []store.OrderRecord,
	positions []exchange.Position,
	instruments []store.InstrumentRecord,
	snapshot exchange.AccountSnapshot,
	full []string,
) ([]string, error) {
	set := make(map[string]struct{})
	for _, current := range openOrders {
		set[current.ExchangeSymbol] = struct{}{}
	}
	for _, current := range localOrders {
		if !orderdomain.State(current.State).Terminal() {
			set[current.ExchangeSymbol] = struct{}{}
		}
	}
	gaps, err := s.Store.OrderSymbolsWithFillGap(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	for _, symbol := range gaps {
		set[symbol] = struct{}{}
	}
	changed, err := s.changedPositionSymbols(ctx, account, positions)
	if err != nil {
		return nil, err
	}
	for _, symbol := range changed {
		set[symbol] = struct{}{}
	}
	if exchange.RuleFor(account.MarketType).PositionFromBalance {
		for _, symbol := range symbolsForAssets(account, instruments, changedAssets(account.Snapshot.Balances, snapshot.Balances)) {
			set[symbol] = struct{}{}
		}
	}
	for _, symbol := range s.nextRotationBatch(account.TradingAccountID, full) {
		set[symbol] = struct{}{}
	}
	delete(set, "")
	return sortedSymbols(set), nil
}

func (s *Service) changedPositionSymbols(
	ctx context.Context,
	account store.TradingAccountRecord,
	positions []exchange.Position,
) ([]string, error) {
	records, err := s.Store.ListPositions(ctx, account.SpaceID, account.TradingAccountID, "")
	if err != nil {
		return nil, err
	}
	local := make(map[string]shared.Decimal, len(records))
	for _, record := range records {
		quantity, parseErr := shared.ParseDecimal(record.SignedQuantity)
		if parseErr != nil {
			return nil, parseErr
		}
		local[record.ExchangeSymbol] = quantity
	}
	var symbols []string
	for _, position := range positions {
		if position.SignedQuantity.Cmp(local[position.ExchangeSymbol]) != 0 {
			symbols = append(symbols, position.ExchangeSymbol)
		}
		delete(local, position.ExchangeSymbol)
	}
	for symbol, quantity := range local {
		if !quantity.IsZero() {
			symbols = append(symbols, symbol)
		}
	}
	return symbols, nil
}

// changedAssets 比较上一份快照和本轮快照。现货没有本地持仓表，只能这样发现变化。
func changedAssets(previous []store.AssetBalance, current []exchange.AssetBalance) map[string]struct{} {
	before := make(map[string]shared.Decimal, len(previous))
	for _, balance := range previous {
		if total, err := shared.ParseDecimal(balance.Total); err == nil {
			before[balance.Asset] = total
		}
	}
	changed := make(map[string]struct{})
	for _, balance := range current {
		if balance.Total.Cmp(before[balance.Asset]) != 0 {
			changed[balance.Asset] = struct{}{}
		}
		delete(before, balance.Asset)
	}
	for asset, total := range before {
		if !total.IsZero() {
			changed[asset] = struct{}{}
		}
	}
	return changed
}

func symbolsForAssets(
	account store.TradingAccountRecord,
	instruments []store.InstrumentRecord,
	assets map[string]struct{},
) []string {
	var symbols []string
	for _, instrument := range instruments {
		if instrument.Status != "TRADING" && instrument.Status != "live" {
			continue
		}
		if !strings.EqualFold(instrument.QuoteAsset, account.SettlementAsset) {
			continue
		}
		if _, ok := assets[instrument.BaseAsset]; ok {
			symbols = append(symbols, instrument.ExchangeSymbol)
		}
	}
	return symbols
}

func (s *Service) nextRotationBatch(tradingAccountID string, full []string) []string {
	if len(full) == 0 {
		return nil
	}
	size := (len(full) + rotationBatches - 1) / rotationBatches
	s.rotationMu.Lock()
	defer s.rotationMu.Unlock()
	if s.rotation == nil {
		s.rotation = make(map[string]int)
	}
	start := s.rotation[tradingAccountID] % len(full)
	s.rotation[tradingAccountID] = start + size
	batch := make([]string, 0, size)
	for index := 0; index < size; index++ {
		batch = append(batch, full[(start+index)%len(full)])
	}
	return batch
}

func fillsAfterCreation(fills []exchange.Fill, createdAt time.Time) []exchange.Fill {
	if createdAt.IsZero() {
		return fills
	}
	kept := make([]exchange.Fill, 0, len(fills))
	for _, fill := range fills {
		if !fill.TradedAt.Before(createdAt) {
			kept = append(kept, fill)
		}
	}
	return kept
}

func sortedSymbols(set map[string]struct{}) []string {
	symbols := make([]string, 0, len(set))
	for symbol := range set {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	return symbols
}
