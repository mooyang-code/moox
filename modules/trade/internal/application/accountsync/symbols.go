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

// rotationBatches 是轮转批次数：每轮多拉全集的十分之一，但不超过 rotationLimit 个标的。
// 491 个标的时 30 秒一轮约 12 分钟走完一圈，换来每次对账最多 100 权重的成交查询。
const (
	rotationBatches = 10
	rotationLimit   = 20
)

// FullSymbols 是账户可能有成交的全部标的：有成交游标、有持仓、有挂单或近期订单的标的，
// 以及现货持有资产对应的交易对。会话启动和手动同步拉这个全集。
func FullSymbols(
	account store.TradingAccountRecord,
	openOrders []exchange.Order,
	localOrders []store.OrderRecord,
	positions []exchange.Position,
	instruments []store.InstrumentRecord,
	snapshot exchange.AccountSnapshot,
) []exchange.SymbolKey {
	set := make(map[exchange.SymbolKey]struct{})
	add := func(key exchange.SymbolKey) {
		if !key.IsZero() {
			set[key] = struct{}{}
		}
	}
	for cursorKey := range account.FillCursors {
		// 游标的键是“市场:交易所代码”；解析不了的键是脏数据，没有标的可拉，忽略。
		if key, err := exchange.ParseSymbolKey(cursorKey); err == nil {
			add(key)
		}
	}
	for _, current := range openOrders {
		add(orderKey(current))
	}
	for _, current := range localOrders {
		add(exchange.NewSymbolKey(current.MarketType, current.ExchangeSymbol))
	}
	for _, position := range positions {
		add(positionKey(position))
	}
	if account.HasBalanceMarket() {
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

func orderKey(current exchange.Order) exchange.SymbolKey {
	return exchange.SymbolKey{Market: current.Market, Symbol: current.ExchangeSymbol}
}

func positionKey(position exchange.Position) exchange.SymbolKey {
	return exchange.SymbolKey{Market: position.Market, Symbol: position.ExchangeSymbol}
}

// fullSymbolCap 是一次完整同步最多拉成交的标的数：491 个标的全拉要 2455 权重，超过永续 1 分钟额度。
// 只有挂单、未结束订单和成交缺口涉及的标的不受限制，其余由轮转在后续对账中补齐。
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
) ([]exchange.SymbolKey, error) {
	full := FullSymbols(account, openOrders, localOrders, positions, instruments, snapshot)
	// 订单和成交缺口涉及的标的必须拉（自己的成交要入账），不受上限；
	// 持仓、余额变化的标的按上限拉，其余由轮转补齐。本地库为空、交易所已有几百个持仓时，
	// 每个持仓都算变化，全拉会超过 1 分钟权重额度，会话会每分钟撞一次 429。
	set := make(map[exchange.SymbolKey]struct{})
	for _, current := range openOrders {
		set[orderKey(current)] = struct{}{}
	}
	var finished []exchange.SymbolKey
	for _, current := range localOrders {
		key := exchange.NewSymbolKey(current.MarketType, current.ExchangeSymbol)
		if orderdomain.State(current.State).Terminal() {
			finished = append(finished, key)
			continue
		}
		set[key] = struct{}{}
	}
	gaps, err := s.Store.OrderSymbolsWithFillGap(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	for _, key := range gaps {
		set[key] = struct{}{}
	}
	delete(set, exchange.SymbolKey{})
	// 可选标的按重要程度排：持仓或余额变化（可能有外部成交）优先，其次是近期已结束的订单，最后是轮转。
	changed, err := s.changedPositionSymbols(ctx, account, positions, 0)
	if err != nil {
		return nil, err
	}
	optional := append([]exchange.SymbolKey(nil), changed...)
	if account.HasBalanceMarket() {
		optional = append(optional, symbolsForAssets(account, instruments, changedAssets(account.Snapshot.Balances, snapshot.Balances))...)
	}
	optional = append(optional, finished...)
	optional = append(optional, s.nextRotationBatch(account.TradingAccountID, full)...)
	for _, key := range optional {
		if len(set) >= fullSymbolCap {
			break
		}
		if !key.IsZero() {
			set[key] = struct{}{}
		}
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
	full []exchange.SymbolKey,
	rotate bool,
	protectAfter int64,
) ([]exchange.SymbolKey, error) {
	set := make(map[exchange.SymbolKey]struct{})
	for _, current := range openOrders {
		set[orderKey(current)] = struct{}{}
	}
	for _, current := range localOrders {
		if !orderdomain.State(current.State).Terminal() {
			set[exchange.NewSymbolKey(current.MarketType, current.ExchangeSymbol)] = struct{}{}
		}
	}
	inconsistent, err := s.inconsistentSymbols(ctx, account, positions, instruments, snapshot, protectAfter)
	if err != nil {
		return nil, err
	}
	for _, key := range inconsistent {
		set[key] = struct{}{}
	}
	if rotate {
		for _, key := range s.nextRotationBatch(account.TradingAccountID, full) {
			set[key] = struct{}{}
		}
	}
	delete(set, exchange.SymbolKey{})
	return sortedSymbols(set), nil
}

// inconsistentSymbols 是本地事实和 REST 不一致、需要补拉成交的标的：成交缺口、持仓变化（保护期内的行不算）、
// 现货余额变化。轻量同步只拉这些。
func (s *Service) inconsistentSymbols(
	ctx context.Context,
	account store.TradingAccountRecord,
	positions []exchange.Position,
	instruments []store.InstrumentRecord,
	snapshot exchange.AccountSnapshot,
	protectAfter int64,
) ([]exchange.SymbolKey, error) {
	set := make(map[exchange.SymbolKey]struct{})
	gaps, err := s.Store.OrderSymbolsWithFillGap(ctx, account.SpaceID, account.TradingAccountID)
	if err != nil {
		return nil, err
	}
	for _, key := range gaps {
		set[key] = struct{}{}
	}
	changed, err := s.changedPositionSymbols(ctx, account, positions, protectAfter)
	if err != nil {
		return nil, err
	}
	for _, key := range changed {
		set[key] = struct{}{}
	}
	if account.HasBalanceMarket() {
		for _, key := range symbolsForAssets(account, instruments, changedAssets(account.Snapshot.Balances, snapshot.Balances)) {
			set[key] = struct{}{}
		}
	}
	delete(set, exchange.SymbolKey{})
	return sortedSymbols(set), nil
}

// changedPositionSymbols 比较本地持仓和 REST 持仓。时间晚于 protectAfter 的本地行在保护期内，不算不一致。
func (s *Service) changedPositionSymbols(
	ctx context.Context,
	account store.TradingAccountRecord,
	positions []exchange.Position,
	protectAfter int64,
) ([]exchange.SymbolKey, error) {
	records, err := s.Store.ListPositions(ctx, account.SpaceID, account.TradingAccountID, exchange.SymbolKey{})
	if err != nil {
		return nil, err
	}
	local := make(map[exchange.SymbolKey]shared.Decimal, len(records))
	protected := make(map[exchange.SymbolKey]struct{})
	for _, record := range records {
		key := exchange.NewSymbolKey(record.MarketType, record.ExchangeSymbol)
		if protectAfter > 0 && record.ExchangeUpdatedAt > protectAfter {
			protected[key] = struct{}{}
		}
		quantity, parseErr := shared.ParseDecimal(record.SignedQuantity)
		if parseErr != nil {
			return nil, parseErr
		}
		local[key] = quantity
	}
	var keys []exchange.SymbolKey
	for _, position := range positions {
		key := positionKey(position)
		if _, ok := protected[key]; ok {
			delete(local, key)
			continue
		}
		if position.SignedQuantity.Cmp(local[key]) != 0 {
			keys = append(keys, key)
		}
		delete(local, key)
	}
	for key, quantity := range local {
		if _, ok := protected[key]; ok {
			continue
		}
		if !quantity.IsZero() {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// changedAssets 比较上一份快照和本轮快照，返回余额有变化的资产。现货没有本地持仓表，只能这样发现变化。
// 同一资产可能出现在多个钱包里，所以按（钱包，资产）比较。
func changedAssets(previous []store.AssetBalance, current []exchange.AssetBalance) map[string]struct{} {
	type balanceKey struct{ wallet, asset string }
	before := make(map[balanceKey]shared.Decimal, len(previous))
	for _, balance := range previous {
		if total, err := shared.ParseDecimal(balance.Total); err == nil {
			before[balanceKey{balance.Wallet, balance.Asset}] = total
		}
	}
	changed := make(map[string]struct{})
	for _, balance := range current {
		key := balanceKey{string(balance.Wallet), balance.Asset}
		if balance.Total.Cmp(before[key]) != 0 {
			changed[balance.Asset] = struct{}{}
		}
		delete(before, key)
	}
	for key, total := range before {
		if !total.IsZero() {
			changed[key.asset] = struct{}{}
		}
	}
	return changed
}

// symbolsForAssets 返回持有这些资产的现货（持仓来自余额的市场）交易对。
func symbolsForAssets(
	account store.TradingAccountRecord,
	instruments []store.InstrumentRecord,
	assets map[string]struct{},
) []exchange.SymbolKey {
	var keys []exchange.SymbolKey
	for _, instrument := range instruments {
		if !exchange.RuleFor(instrument.MarketType).PositionFromBalance {
			continue
		}
		if instrument.Status != "TRADING" && instrument.Status != "live" {
			continue
		}
		if !strings.EqualFold(instrument.QuoteAsset, account.SettlementAsset) {
			continue
		}
		if _, ok := assets[instrument.BaseAsset]; ok {
			keys = append(keys, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
		}
	}
	return keys
}

func (s *Service) nextRotationBatch(tradingAccountID string, full []exchange.SymbolKey) []exchange.SymbolKey {
	if len(full) == 0 {
		return nil
	}
	size := min((len(full)+rotationBatches-1)/rotationBatches, rotationLimit)
	s.rotationMu.Lock()
	defer s.rotationMu.Unlock()
	if s.rotation == nil {
		s.rotation = make(map[string]int)
	}
	start := s.rotation[tradingAccountID] % len(full)
	s.rotation[tradingAccountID] = start + size
	batch := make([]exchange.SymbolKey, 0, size)
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

func sortedSymbols(set map[exchange.SymbolKey]struct{}) []exchange.SymbolKey {
	keys := make([]exchange.SymbolKey, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}
