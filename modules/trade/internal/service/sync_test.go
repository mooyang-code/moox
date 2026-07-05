package service

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

type syncCoordinatorStore struct {
	Store
	accounts           []*Account
	channels           map[string]*TradeChannel
	apiKeys            map[string]*APIKey
	balancesByAccount  map[string][]*Balance
	positionsByAccount map[string][]*Position
	ordersByAccount    map[string][]*Order
	tradesByAccount    map[string][]*Trade
	cursors            map[string]*SyncCursor
	upsertedBalances   int
	replacedPositions  int
}

func (s *syncCoordinatorStore) ListAccounts(ctx context.Context, spaceID string, f AccountFilter, page Page) ([]*Account, int, error) {
	items := make([]*Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		if f.AccountType != "" && account.AccountType != f.AccountType {
			continue
		}
		items = append(items, account)
	}
	start := page.Offset()
	if start >= len(items) {
		return nil, len(items), nil
	}
	end := start + page.PageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], len(items), nil
}

func (s *syncCoordinatorStore) GetAccount(ctx context.Context, spaceID, accountID string) (*Account, error) {
	for _, account := range s.accounts {
		if account.AccountID == accountID {
			return account, nil
		}
	}
	return nil, ErrNotFound
}

func (s *syncCoordinatorStore) GetChannel(ctx context.Context, spaceID, channelID string) (*TradeChannel, error) {
	if channel := s.channels[channelID]; channel != nil {
		return channel, nil
	}
	return nil, ErrNotFound
}

func (s *syncCoordinatorStore) GetAPIKey(ctx context.Context, spaceID, apiKeyID string) (*APIKey, error) {
	if key := s.apiKeys[apiKeyID]; key != nil {
		return key, nil
	}
	return nil, ErrNotFound
}

func (s *syncCoordinatorStore) UpsertBalances(ctx context.Context, spaceID string, balances []*Balance) error {
	if s.balancesByAccount == nil {
		s.balancesByAccount = map[string][]*Balance{}
	}
	for _, balance := range balances {
		cp := *balance
		cp.SpaceID = spaceID
		s.balancesByAccount[balance.AccountID] = append(s.balancesByAccount[balance.AccountID], &cp)
	}
	s.upsertedBalances += len(balances)
	return nil
}

func (s *syncCoordinatorStore) GetBalances(ctx context.Context, spaceID, accountID string, currencies []string) ([]*Balance, error) {
	return append([]*Balance(nil), s.balancesByAccount[accountID]...), nil
}

func (s *syncCoordinatorStore) ReplacePositions(ctx context.Context, spaceID, accountID, symbol string, positions []*Position) error {
	if s.positionsByAccount == nil {
		s.positionsByAccount = map[string][]*Position{}
	}
	s.positionsByAccount[accountID] = append([]*Position(nil), positions...)
	s.replacedPositions += len(positions)
	return nil
}

func (s *syncCoordinatorStore) ListPositions(ctx context.Context, spaceID, accountID, symbol string) ([]*Position, error) {
	return append([]*Position(nil), s.positionsByAccount[accountID]...), nil
}

func (s *syncCoordinatorStore) ListOrders(ctx context.Context, spaceID string, f OrderFilter, page Page) ([]*Order, int, error) {
	var out []*Order
	for _, order := range s.ordersByAccount[f.AccountID] {
		if f.Symbol != "" && order.Symbol != f.Symbol {
			continue
		}
		out = append(out, order)
	}
	return out, len(out), nil
}

func (s *syncCoordinatorStore) UpsertOrders(ctx context.Context, spaceID string, orders []*Order) error {
	if s.ordersByAccount == nil {
		s.ordersByAccount = map[string][]*Order{}
	}
	for _, order := range orders {
		cp := *order
		cp.SpaceID = spaceID
		s.ordersByAccount[order.AccountID] = append(s.ordersByAccount[order.AccountID], &cp)
	}
	return nil
}

func (s *syncCoordinatorStore) ListTrades(ctx context.Context, spaceID string, f TradeFilter, page Page) ([]*Trade, int, error) {
	var out []*Trade
	for _, trade := range s.tradesByAccount[f.AccountID] {
		if f.Symbol != "" && trade.Symbol != f.Symbol {
			continue
		}
		out = append(out, trade)
	}
	return out, len(out), nil
}

func (s *syncCoordinatorStore) AppendTrades(ctx context.Context, spaceID string, trades []*Trade) error {
	if s.tradesByAccount == nil {
		s.tradesByAccount = map[string][]*Trade{}
	}
	for _, trade := range trades {
		cp := *trade
		cp.SpaceID = spaceID
		s.tradesByAccount[trade.AccountID] = append(s.tradesByAccount[trade.AccountID], &cp)
	}
	return nil
}

func (s *syncCoordinatorStore) GetSyncCursor(ctx context.Context, spaceID, accountID string, syncType SyncType, symbol string) (*SyncCursor, error) {
	if cursor := s.cursors[syncCursorTestKey(accountID, syncType, symbol)]; cursor != nil {
		return cursor, nil
	}
	return nil, ErrNotFound
}

func (s *syncCoordinatorStore) UpsertSyncCursor(ctx context.Context, spaceID string, cursor *SyncCursor) error {
	if s.cursors == nil {
		s.cursors = map[string]*SyncCursor{}
	}
	cp := *cursor
	cp.SpaceID = spaceID
	s.cursors[syncCursorTestKey(cursor.AccountID, cursor.SyncType, cursor.Symbol)] = &cp
	return nil
}

func syncCursorTestKey(accountID string, syncType SyncType, symbol string) string {
	return accountID + "|" + string(syncType) + "|" + symbol
}

type syncCoordinatorAdapter struct {
	exchange.ExchangeAdapter
	balances  []exchange.Balance
	positions []exchange.Position
	orders    []exchange.Order
	trades    []exchange.Trade
}

func (a *syncCoordinatorAdapter) GetBalances(ctx context.Context, cred exchange.Credential, market exchange.MarketType, currencies []string) ([]exchange.Balance, error) {
	return a.balances, nil
}

func (a *syncCoordinatorAdapter) ListPositions(ctx context.Context, cred exchange.Credential, market exchange.MarketType, symbol string) ([]exchange.Position, error) {
	return a.positions, nil
}

func (a *syncCoordinatorAdapter) ListOrders(ctx context.Context, cred exchange.Credential, req *exchange.ListOrdersReq) ([]exchange.Order, error) {
	return a.orders, nil
}

func (a *syncCoordinatorAdapter) ListTrades(ctx context.Context, cred exchange.Credential, req *exchange.ListTradesReq) ([]exchange.Trade, error) {
	return a.trades, nil
}

func TestSyncSnapshotsSyncsBalancesForAllAccountsAndPositionsForSwap(t *testing.T) {
	store := &syncCoordinatorStore{
		accounts: []*Account{
			{AccountID: "spot_1", AccountType: AccountSpot, ChannelID: "ch_spot"},
			{AccountID: "swap_1", AccountType: AccountSwap, ChannelID: "ch_swap"},
		},
		channels: map[string]*TradeChannel{
			"ch_spot": {ChannelID: "ch_spot", Exchange: "binance", MarketType: "spot", APIKeyID: "ak_spot"},
			"ch_swap": {ChannelID: "ch_swap", Exchange: "binance", MarketType: "swap", APIKeyID: "ak_swap"},
		},
		apiKeys: map[string]*APIKey{
			"ak_spot": {APIKeyID: "ak_spot", APIKey: "key", APISecret: "secret"},
			"ak_swap": {APIKeyID: "ak_swap", APIKey: "key", APISecret: "secret"},
		},
	}
	adapter := &syncCoordinatorAdapter{
		balances:  []exchange.Balance{{Currency: "USDT", Available: "1", Total: "1"}},
		positions: []exchange.Position{{Symbol: "BTCUSDT", PosSide: "long", Quantity: "0.01"}},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return adapter, nil
	}))

	report, err := svc.SyncAllSnapshots(context.Background(), SyncOptions{SpaceID: "crypto", PageSize: 100})
	if err != nil {
		t.Fatalf("SyncAllSnapshots returned error: %v", err)
	}
	if report.BalancesSynced != 2 {
		t.Fatalf("BalancesSynced=%d, want 2", report.BalancesSynced)
	}
	if report.PositionsSynced != 1 {
		t.Fatalf("PositionsSynced=%d, want 1", report.PositionsSynced)
	}
}

func TestHistorySymbolsPreferExistingLocalAndNonZeroBalanceSymbols(t *testing.T) {
	store := &syncCoordinatorStore{
		balancesByAccount: map[string][]*Balance{
			"acc_1": {
				{Currency: "GALA", Total: "1225.773"},
				{Currency: "USDT", Total: "10"},
				{Currency: "SHIB", Total: "0.0001"},
			},
		},
		ordersByAccount: map[string][]*Order{
			"acc_1": {{Symbol: "SOLUSDT"}},
		},
		tradesByAccount: map[string][]*Trade{
			"acc_1": {{Symbol: "BTCUSDT"}},
		},
	}
	svc := New("trade", WithStore(store))
	got := svc.historySymbols(context.Background(), "crypto", &Account{AccountID: "acc_1", BaseCurrency: "USDT"}, 10)
	want := []string{"BTCUSDT", "GALAUSDT", "SHIBUSDT", "SOLUSDT"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symbols=%v, want %v", got, want)
	}
}

func TestSyncTradingHistoryAdvancesOrderAndTradeCursors(t *testing.T) {
	now := time.UnixMilli(1_800_000)
	store := &syncCoordinatorStore{
		accounts: []*Account{{AccountID: "acc_1", AccountType: AccountSpot, ChannelID: "ch_1", BaseCurrency: "USDT"}},
		channels: map[string]*TradeChannel{"ch_1": {ChannelID: "ch_1", Exchange: "binance", MarketType: "spot", APIKeyID: "ak_1"}},
		apiKeys:  map[string]*APIKey{"ak_1": {APIKeyID: "ak_1", APIKey: "key", APISecret: "secret"}},
		balancesByAccount: map[string][]*Balance{
			"acc_1": {{Currency: "GALA", Total: "1"}},
		},
	}
	adapter := &syncCoordinatorAdapter{
		orders: []exchange.Order{{ExchangeOrderID: "1001", Symbol: "GALAUSDT", Market: exchange.MarketSpot}},
		trades: []exchange.Trade{{TradeID: "2001", Symbol: "GALAUSDT", TradedAt: now.UnixMilli()}},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return adapter, nil
	}))

	report, err := svc.SyncTradingHistory(context.Background(), SyncOptions{
		SpaceID: "crypto", WindowHours: 24, PageSize: 500, MaxSymbolsPerRun: 10, Now: now,
	})
	if err != nil {
		t.Fatalf("SyncTradingHistory returned error: %v", err)
	}
	if report.OrdersSynced != 1 || report.TradesSynced != 1 {
		t.Fatalf("report=%+v, want one order and one trade", report)
	}
	orderCursor, _ := store.GetSyncCursor(context.Background(), "crypto", "acc_1", SyncTypeOrders, "GALAUSDT")
	tradeCursor, _ := store.GetSyncCursor(context.Background(), "crypto", "acc_1", SyncTypeTrades, "GALAUSDT")
	if orderCursor.CursorEndMS != now.UnixMilli() || tradeCursor.CursorEndMS != now.UnixMilli() {
		t.Fatalf("cursors order=%+v trade=%+v, want end=%d", orderCursor, tradeCursor, now.UnixMilli())
	}
}

func TestSyncAllSnapshotsSkipsPositionsWhenSectionDisabled(t *testing.T) {
	store := &syncCoordinatorStore{
		accounts: []*Account{{AccountID: "swap_1", AccountType: AccountSwap, ChannelID: "ch_1"}},
		channels: map[string]*TradeChannel{"ch_1": {ChannelID: "ch_1", Exchange: "binance", MarketType: "swap", APIKeyID: "ak_1"}},
		apiKeys:  map[string]*APIKey{"ak_1": {APIKeyID: "ak_1", APIKey: "key", APISecret: "secret"}},
	}
	adapter := &syncCoordinatorAdapter{
		balances:  []exchange.Balance{{Currency: "USDT", Total: "1"}},
		positions: []exchange.Position{{Symbol: "BTCUSDT", PosSide: "long", Quantity: "0.01"}},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) { return adapter, nil }))
	report, err := svc.SyncAllSnapshots(context.Background(), SyncOptions{
		SpaceID:  "crypto",
		Sections: map[SyncType]bool{SyncTypeBalances: true},
	})
	if err != nil {
		t.Fatalf("SyncAllSnapshots returned error: %v", err)
	}
	if report.BalancesSynced != 1 || report.PositionsSynced != 0 {
		t.Fatalf("report=%+v, want balances only", report)
	}
}
