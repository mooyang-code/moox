package service

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

type execOrderStore struct {
	account      *Account
	channel      *TradeChannel
	apiKey       *APIKey
	orders       map[string]*Order
	byClient     map[string]*Order
	frozenDeltas []string
	ops          []*OrderOperation
	trades       []*Trade
}

func (s *execOrderStore) GetAccount(_ context.Context, _, accountID string) (*Account, error) {
	if s.account == nil || s.account.AccountID != accountID {
		return nil, ErrNotFound
	}
	cp := *s.account
	return &cp, nil
}

func (s *execOrderStore) GetChannel(_ context.Context, _, channelID string) (*TradeChannel, error) {
	if s.channel == nil || s.channel.ChannelID != channelID {
		return nil, ErrNotFound
	}
	cp := *s.channel
	return &cp, nil
}

func (s *execOrderStore) GetAPIKey(_ context.Context, _, apiKeyID string) (*APIKey, error) {
	if s.apiKey == nil || s.apiKey.APIKeyID != apiKeyID {
		return nil, ErrNotFound
	}
	cp := *s.apiKey
	return &cp, nil
}

func (s *execOrderStore) AdjustFrozen(_ context.Context, _, _, _, delta string) error {
	s.frozenDeltas = append(s.frozenDeltas, delta)
	return nil
}

func (s *execOrderStore) SaveOrder(_ context.Context, spaceID string, o *Order) error {
	if s.orders == nil {
		s.orders = map[string]*Order{}
		s.byClient = map[string]*Order{}
	}
	cp := *o
	cp.SpaceID = spaceID
	s.orders[o.OrderID] = &cp
	if o.ClientOrderID != "" {
		s.byClient[o.ClientOrderID] = &cp
	}
	return nil
}

func (s *execOrderStore) UpdateOrder(_ context.Context, spaceID string, o *Order) error {
	if s.orders[o.OrderID] == nil {
		return ErrNotFound
	}
	cp := *o
	cp.SpaceID = spaceID
	s.orders[o.OrderID] = &cp
	if o.ClientOrderID != "" {
		s.byClient[o.ClientOrderID] = &cp
	}
	return nil
}

func (s *execOrderStore) GetOrder(_ context.Context, _, orderID, clientOrderID string) (*Order, error) {
	if orderID != "" {
		if o := s.orders[orderID]; o != nil {
			cp := *o
			return &cp, nil
		}
	}
	if clientOrderID != "" {
		if o := s.byClient[clientOrderID]; o != nil {
			cp := *o
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (s *execOrderStore) AppendOrderOperation(_ context.Context, _ string, op *OrderOperation) error {
	cp := *op
	s.ops = append(s.ops, &cp)
	return nil
}

func (s *execOrderStore) UpdateOrderOperation(context.Context, string, *OrderOperation) error {
	return nil
}
func (s *execOrderStore) AppendTrades(_ context.Context, spaceID string, trades []*Trade) error {
	for _, tr := range trades {
		cp := *tr
		cp.SpaceID = spaceID
		s.trades = append(s.trades, &cp)
	}
	return nil
}
func (s *execOrderStore) AppendFundFlows(context.Context, string, []*FundFlow) error { return nil }
func (s *execOrderStore) CreateAccount(context.Context, string, *Account) error      { return nil }
func (s *execOrderStore) UpdateAccount(context.Context, string, *Account) error      { return nil }
func (s *execOrderStore) DeleteAccount(context.Context, string, string) error        { return nil }
func (s *execOrderStore) ListAccounts(context.Context, string, AccountFilter, Page) ([]*Account, int, error) {
	return nil, 0, nil
}
func (s *execOrderStore) GetBalances(context.Context, string, string, []string) ([]*Balance, error) {
	return nil, nil
}
func (s *execOrderStore) UpsertBalances(context.Context, string, []*Balance) error { return nil }
func (s *execOrderStore) ListFundFlows(context.Context, string, FundFlowFilter, Page) ([]*FundFlow, int, error) {
	return nil, 0, nil
}
func (s *execOrderStore) CreateAPIKey(context.Context, string, *APIKey) error { return nil }
func (s *execOrderStore) DeleteAPIKey(context.Context, string, string) error  { return nil }
func (s *execOrderStore) ListAPIKeys(context.Context, string, string) ([]*APIKey, error) {
	return nil, nil
}
func (s *execOrderStore) CreateChannel(context.Context, string, *TradeChannel) error { return nil }
func (s *execOrderStore) UpdateChannel(context.Context, string, *TradeChannel) error { return nil }
func (s *execOrderStore) DeleteChannel(context.Context, string, string) error        { return nil }
func (s *execOrderStore) ListChannels(context.Context, string, ChannelFilter, Page) ([]*TradeChannel, int, error) {
	return nil, 0, nil
}
func (s *execOrderStore) UpsertOrders(context.Context, string, []*Order) error { return nil }
func (s *execOrderStore) ListOrders(context.Context, string, OrderFilter, Page) ([]*Order, int, error) {
	return nil, 0, nil
}
func (s *execOrderStore) ListTrades(context.Context, string, TradeFilter, Page) ([]*Trade, int, error) {
	return s.trades, len(s.trades), nil
}
func (s *execOrderStore) UpsertPositions(context.Context, string, []*Position) error { return nil }
func (s *execOrderStore) ReplacePositions(context.Context, string, string, string, []*Position) error {
	return nil
}
func (s *execOrderStore) ListPositions(context.Context, string, string, string) ([]*Position, error) {
	return nil, nil
}
func (s *execOrderStore) GetSyncCursor(context.Context, string, string, SyncType, string) (*SyncCursor, error) {
	return nil, ErrNotFound
}
func (s *execOrderStore) UpsertSyncCursor(context.Context, string, *SyncCursor) error { return nil }
func (s *execOrderStore) ListSyncCursors(context.Context, string, string, SyncType) ([]*SyncCursor, error) {
	return nil, nil
}

type fakeExecAdapter struct {
	fakePositionAdapter
	placeResult *exchange.OrderResult
	placeErr    error
	cancelRes   *exchange.OrderResult
	cancelErr   error
	amendRes    *exchange.OrderResult
	amendErr    error
	instruments []exchange.Instrument
}

func (a *fakeExecAdapter) GetInstruments(context.Context, exchange.MarketType) ([]exchange.Instrument, error) {
	return a.instruments, nil
}

func (a *fakeExecAdapter) PlaceOrder(context.Context, exchange.Credential, *exchange.PlaceOrderReq) (*exchange.OrderResult, error) {
	if a.placeErr != nil {
		return nil, a.placeErr
	}
	if a.placeResult != nil {
		return a.placeResult, nil
	}
	return &exchange.OrderResult{ExchangeOrderID: "ex-100", Status: exchange.StatusSubmitted}, nil
}

func (a *fakeExecAdapter) CancelOrder(context.Context, exchange.Credential, *exchange.CancelOrderReq) (*exchange.OrderResult, error) {
	if a.cancelErr != nil {
		return nil, a.cancelErr
	}
	if a.cancelRes != nil {
		return a.cancelRes, nil
	}
	return &exchange.OrderResult{Status: exchange.StatusCanceled}, nil
}

func (a *fakeExecAdapter) AmendOrder(context.Context, exchange.Credential, *exchange.AmendOrderReq) (*exchange.OrderResult, error) {
	if a.amendErr != nil {
		return nil, a.amendErr
	}
	if a.amendRes != nil {
		return a.amendRes, nil
	}
	return &exchange.OrderResult{Status: exchange.StatusSubmitted}, nil
}

func (a *fakeExecAdapter) CancelAllOrders(context.Context, exchange.Credential, exchange.MarketType, string) (int, error) {
	return 0, nil
}

func (a *fakeExecAdapter) SetLeverage(context.Context, exchange.Credential, exchange.MarketType, string, string) error {
	return nil
}

func (a *fakeExecAdapter) Ping(context.Context, exchange.Credential) (int64, error) { return 12, nil }

func newExecOrderService(t *testing.T, adapter *fakeExecAdapter) (*OrderService, *execOrderStore) {
	t.Helper()
	store := &execOrderStore{
		account: &Account{AccountID: "acc_1"},
		channel: &TradeChannel{
			ChannelID: "ch_1", Exchange: "binance", MarketType: "spot",
			AccountID: "acc_1", APIKeyID: "ak_1",
		},
		apiKey: &APIKey{APIKeyID: "ak_1", APIKey: "k", APISecret: "s"},
	}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return adapter, nil
	}))
	return svc.Order, store
}

func TestPlaceOrderExec_SpotLimitSuccess_ShouldSubmitAndAudit(t *testing.T) {
	adapter := &fakeExecAdapter{instruments: []exchange.Instrument{{
		Symbol: "BTCUSDT", LotSize: "0.001", MinQty: "0.001", MinNotional: "5",
	}}}
	svc, store := newExecOrderService(t, adapter)
	ctx := context.Background()
	got, err := svc.PlaceOrderExec(ctx, "crypto", "ch_1", &exchange.PlaceOrderReq{
		Symbol: "BTCUSDT", Side: exchange.SideBuy, Type: exchange.TypeLimit,
		Price: "100", Quantity: "1", ClientOrderID: "cli-1",
	}, "op-1")
	require.NoError(t, err)
	assert.Equal(t, "ex-100", got.ExchangeOrderID)
	assert.Equal(t, int(exchange.StatusSubmitted), got.Status)
	assert.NotEmpty(t, store.frozenDeltas)
	assert.GreaterOrEqual(t, len(store.ops), 2)
}

func TestPlaceOrderExec_AdapterReject_ShouldMarkRejected(t *testing.T) {
	adapter := &fakeExecAdapter{
		placeErr: errors.New("insufficient balance"),
		instruments: []exchange.Instrument{{
			Symbol: "BTCUSDT", LotSize: "0.001", MinQty: "0.001",
		}},
	}
	svc, store := newExecOrderService(t, adapter)
	got, err := svc.PlaceOrderExec(context.Background(), "crypto", "ch_1", &exchange.PlaceOrderReq{
		Symbol: "BTCUSDT", Side: exchange.SideBuy, Type: exchange.TypeLimit,
		Price: "100", Quantity: "1",
	}, "op-1")
	assert.Error(t, err)
	assert.Equal(t, int(exchange.StatusRejected), got.Status)
	assert.Contains(t, got.RejectReason, "insufficient balance")
	assert.GreaterOrEqual(t, len(store.ops), 2)
}

func TestPlaceOrderExec_InvalidParams_ShouldReject(t *testing.T) {
	svc, _ := newExecOrderService(t, &fakeExecAdapter{})
	_, err := svc.PlaceOrderExec(context.Background(), "crypto", "", nil, "")
	assert.ErrorIs(t, err, ErrInvalidParam)
}

func TestCancelOrderExec_Success_ShouldUpdateStatus(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, store := newExecOrderService(t, adapter)
	ctx := context.Background()
	existing := &Order{
		OrderID: "o-1", ClientOrderID: "cli-1", AccountID: "acc_1", ChannelID: "ch_1",
		Exchange: "binance", Symbol: "BTCUSDT", Side: "buy", Quantity: "1", Price: "100",
		Status: int(exchange.StatusSubmitted),
	}
	require.NoError(t, svc.store.SaveOrder(ctx, "crypto", existing))
	got, err := svc.CancelOrderExec(ctx, "crypto", "ch_1", &exchange.CancelOrderReq{OrderID: "o-1"}, "op-1")
	require.NoError(t, err)
	assert.Equal(t, int(exchange.StatusCanceled), got.Status)
	assert.GreaterOrEqual(t, len(store.ops), 1)
}

func TestCancelOrderExec_AdapterError_ShouldRecordFailure(t *testing.T) {
	adapter := &fakeExecAdapter{cancelErr: errors.New("cancel failed")}
	svc, store := newExecOrderService(t, adapter)
	ctx := context.Background()
	require.NoError(t, svc.store.SaveOrder(ctx, "crypto", &Order{
		OrderID: "o-2", AccountID: "acc_1", ChannelID: "ch_1", Symbol: "BTCUSDT", Status: 1,
	}))
	got, err := svc.CancelOrderExec(ctx, "crypto", "ch_1", &exchange.CancelOrderReq{OrderID: "o-2"}, "op-1")
	assert.Error(t, err)
	assert.NotNil(t, got)
	assert.GreaterOrEqual(t, len(store.ops), 1)
	assert.Equal(t, 2, store.ops[len(store.ops)-1].OpStatus)
}

func TestAmendOrderExec_Success_ShouldUpdatePrice(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, _ := newExecOrderService(t, adapter)
	ctx := context.Background()
	require.NoError(t, svc.store.SaveOrder(ctx, "crypto", &Order{
		OrderID: "o-3", AccountID: "acc_1", ChannelID: "ch_1", Symbol: "BTCUSDT",
		Price: "100", Quantity: "1", Status: int(exchange.StatusSubmitted),
	}))
	got, err := svc.AmendOrderExec(ctx, "crypto", "ch_1", &exchange.AmendOrderReq{
		OrderID: "o-3", NewPrice: "110",
	}, "op-1")
	require.NoError(t, err)
	assert.Equal(t, "110", got.Price)
}

func TestApplyFills_PartialFill_ShouldUpdateOrderAndTrades(t *testing.T) {
	adapter := &fakeExecAdapter{}
	svc, store := newExecOrderService(t, adapter)
	ctx := context.Background()
	require.NoError(t, svc.store.SaveOrder(ctx, "crypto", &Order{
		OrderID: "o-4", AccountID: "acc_1", ChannelID: "ch_1", Exchange: "binance",
		Symbol: "BTCUSDT", Side: "buy", Quantity: "2", Price: "100", Status: int(exchange.StatusSubmitted),
	}))
	err := svc.ApplyFills(ctx, "crypto", "o-4", []*exchange.Trade{{
		Price: "100", Quantity: "1", Fee: "0.1", FeeCurrency: "USDT",
	}})
	require.NoError(t, err)
	updated, err := svc.store.GetOrder(ctx, "crypto", "o-4", "")
	require.NoError(t, err)
	assert.Equal(t, "1", updated.FilledQty)
	assert.Equal(t, int(exchange.StatusPartiallyFilled), updated.Status)
	assert.Len(t, store.trades, 1)
}

func TestSplitSymbol_KnownPairs_ShouldSplit(t *testing.T) {
	base, quote := splitSymbol("BTCUSDT")
	assert.Equal(t, "BTC", base)
	assert.Equal(t, "USDT", quote)
	base, quote = splitSymbol("ETHBTC")
	assert.Equal(t, "ETH", base)
	assert.Equal(t, "BTC", quote)
}

func TestFreezeCost_BuyLimit_ShouldFreezeQuote(t *testing.T) {
	cur, amt, err := freezeCost("buy", "BTCUSDT", "100", "2", "")
	require.NoError(t, err)
	assert.Equal(t, "USDT", cur)
	assert.Equal(t, "200", amt)
}

func TestFreezeCost_Sell_ShouldFreezeBase(t *testing.T) {
	cur, amt, err := freezeCost("sell", "BTCUSDT", "100", "1.5", "")
	require.NoError(t, err)
	assert.Equal(t, "BTC", cur)
	assert.Equal(t, "1.5", amt)
}

func TestFreezeCost_MarketBuyByAmount_ShouldUseAmount(t *testing.T) {
	cur, amt, err := freezeCost("buy", "BTCUSDT", "0", "0", "50")
	require.NoError(t, err)
	assert.Equal(t, "USDT", cur)
	assert.Equal(t, "50", amt)
}

func TestAddSubMulDivSvc_ShouldCompute(t *testing.T) {
	sum, err := addSvc("1.5", "2.5")
	require.NoError(t, err)
	assert.Equal(t, "4", sum)
	diff, err := subSvc("5", "2")
	require.NoError(t, err)
	assert.Equal(t, "3", diff)
	prod, err := mulSvc("2", "3")
	require.NoError(t, err)
	assert.Equal(t, "6", prod)
	quot, err := divSvcSafe("10", "4")
	require.NoError(t, err)
	assert.Equal(t, "2.5", quot)
	zero, err := divSvcSafe("10", "0")
	require.NoError(t, err)
	assert.Equal(t, "0", zero)
}

func TestRemainingFreeze_PartialFill_ShouldShrink(t *testing.T) {
	cur, amt, err := remainingFreeze(&Order{
		Side: "buy", Symbol: "BTCUSDT", Price: "100", Quantity: "2", FilledQty: "0.5",
	})
	require.NoError(t, err)
	assert.Equal(t, "USDT", cur)
	assert.Equal(t, "150", amt)
}
