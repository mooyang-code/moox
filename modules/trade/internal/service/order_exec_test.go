package service_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/mooyang-code/moox/modules/trade/internal/service/dao"
	tradeschema "github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const svcEncKey = "moox-cloud-secret-key-32bytes"

var errFakePlace = errors.New("fake: place order rejected by exchange")

// fakeAdapter 用于阶段4编排测试：PlaceOrder/Cancel 成功，GetBalances 返回固定快照。
type fakeAdapter struct {
	exchange.ExchangeAdapter
	placeStatus   exchange.OrderStatus
	cancelStatus  exchange.OrderStatus
	placeErr      error
	exOrderID     string
	lastPlaceReq  *exchange.PlaceOrderReq
	lastCancelReq *exchange.CancelOrderReq
}

func (f *fakeAdapter) Name() string { return "fake" }
func (f *fakeAdapter) Ping(ctx context.Context, cred exchange.Credential) (int64, error) {
	return 1, nil
}
func (f *fakeAdapter) GetBalances(ctx context.Context, cred exchange.Credential, market exchange.MarketType, currencies []string) ([]exchange.Balance, error) {
	return nil, nil
}
func (f *fakeAdapter) PlaceOrder(ctx context.Context, cred exchange.Credential, req *exchange.PlaceOrderReq) (*exchange.OrderResult, error) {
	f.lastPlaceReq = req
	if f.placeErr != nil {
		return nil, f.placeErr
	}
	return &exchange.OrderResult{
		OrderID: req.ClientOrderID, ClientOrderID: req.ClientOrderID,
		ExchangeOrderID: f.exOrderID, Status: f.placeStatus,
	}, nil
}
func (f *fakeAdapter) CancelOrder(ctx context.Context, cred exchange.Credential, req *exchange.CancelOrderReq) (*exchange.OrderResult, error) {
	f.lastCancelReq = req
	return &exchange.OrderResult{OrderID: req.OrderID, ExchangeOrderID: f.exOrderID, Status: f.cancelStatus}, nil
}

func newSvcDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "trade_svc.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(tradeschema.AllSQL()).Error)
	return db
}

func newSvcWithFake(db *gorm.DB, fa *fakeAdapter) *service.Service {
	store := dao.New(db, svcEncKey)
	return service.New("trade",
		service.WithStore(store),
		service.WithExchangeFactory(func(string) (exchange.ExchangeAdapter, error) { return fa, nil }),
	)
}

func setupTradeAccount(t *testing.T, db *gorm.DB, sid string) (accountID, channelID string) {
	t.Helper()
	store := dao.New(db, svcEncKey)
	acc := &service.Account{AccountID: "acc_test", UserID: "u1", AccountName: "test", AccountType: "spot"}
	require.NoError(t, store.CreateAccount(context.Background(), sid, acc))
	require.NoError(t, store.UpsertBalances(context.Background(), sid, []*service.Balance{{
		AccountID: acc.AccountID, Currency: "USDT", Available: "1000", Frozen: "0", Total: "1000",
	}}))
	ch := &service.TradeChannel{ChannelID: "ch_test", AccountID: acc.AccountID, ChannelName: "test", Exchange: "fake", MarketType: "spot"}
	require.NoError(t, store.CreateChannel(context.Background(), sid, ch))
	return acc.AccountID, ch.ChannelID
}

func TestPlaceOrder_FreezeAndAudit(t *testing.T) {
	db := newSvcDB(t)
	sid := "sp1"
	accountID, channelID := setupTradeAccount(t, db, sid)
	fa := &fakeAdapter{placeStatus: exchange.StatusSubmitted, exOrderID: "EX-1001"}
	svc := newSvcWithFake(db, fa)

	o, err := svc.Order.PlaceOrder(context.Background(), sid, channelID, &exchange.PlaceOrderReq{
		Market: "spot", Symbol: "BTCUSDT", Side: "buy", Type: "limit",
		Price: "50000", Quantity: "0.01",
	})
	require.NoError(t, err)
	require.Equal(t, "EX-1001", o.ExchangeOrderID)
	require.Equal(t, int(exchange.StatusSubmitted), o.Status)

	store := dao.New(db, svcEncKey)
	bs, err := store.GetBalances(context.Background(), sid, accountID, []string{"USDT"})
	require.NoError(t, err)
	require.Len(t, bs, 1)
	require.Equal(t, "500", bs[0].Available)
	require.Equal(t, "500", bs[0].Frozen)

	got, err := store.GetOrder(context.Background(), sid, o.OrderID, "")
	require.NoError(t, err)
	require.Equal(t, "0.01", got.Quantity)
	require.Equal(t, "BTCUSDT", got.Symbol)
}

func TestPlaceOrder_AdapterFail_UnfreezeAndReject(t *testing.T) {
	db := newSvcDB(t)
	sid := "sp1"
	accountID, channelID := setupTradeAccount(t, db, sid)
	fa := &fakeAdapter{placeErr: errFakePlace}
	svc := newSvcWithFake(db, fa)

	_, err := svc.Order.PlaceOrder(context.Background(), sid, channelID, &exchange.PlaceOrderReq{
		Market: "spot", Symbol: "BTCUSDT", Side: "buy", Type: "limit",
		Price: "50000", Quantity: "0.01",
	})
	require.Error(t, err)

	store := dao.New(db, svcEncKey)
	bs, err := store.GetBalances(context.Background(), sid, accountID, []string{"USDT"})
	require.NoError(t, err)
	require.Equal(t, "1000", bs[0].Available)
	require.Equal(t, "0", bs[0].Frozen)
}

func TestApplyFills_FullFill_AdjustsBalance(t *testing.T) {
	db := newSvcDB(t)
	sid := "sp1"
	accountID, channelID := setupTradeAccount(t, db, sid)
	fa := &fakeAdapter{placeStatus: exchange.StatusSubmitted, exOrderID: "EX-2001"}
	svc := newSvcWithFake(db, fa)

	o, err := svc.Order.PlaceOrder(context.Background(), sid, channelID, &exchange.PlaceOrderReq{
		Market: "spot", Symbol: "BTCUSDT", Side: "buy", Type: "limit",
		Price: "50000", Quantity: "0.01",
	})
	require.NoError(t, err)

	err = svc.Order.ApplyFills(context.Background(), sid, o.OrderID, []*exchange.Trade{{
		TradeID: "tr1", ExchangeTradeID: "EXTR1", Price: "50000", Quantity: "0.01",
		Fee: "0.0005", FeeCurrency: "BTC", Role: "taker",
	}})
	require.NoError(t, err)

	store := dao.New(db, svcEncKey)
	usdt, _ := store.GetBalances(context.Background(), sid, accountID, []string{"USDT"})
	require.Equal(t, "0", usdt[0].Frozen)

	btc, _ := store.GetBalances(context.Background(), sid, accountID, []string{"BTC"})
	require.Len(t, btc, 1)
	// 所得 0.01 BTC - 手续费 0.0005 BTC = 0.0095
	require.Equal(t, "0.0095", btc[0].Total)

	got, _ := store.GetOrder(context.Background(), sid, o.OrderID, "")
	require.Equal(t, int(exchange.StatusFilled), got.Status)
	require.Equal(t, "0.01", got.FilledQty)
	require.Equal(t, "50000", got.AvgPrice)
}

func TestCancelOrder_UnfreezeRemaining(t *testing.T) {
	db := newSvcDB(t)
	sid := "sp1"
	accountID, channelID := setupTradeAccount(t, db, sid)
	fa := &fakeAdapter{placeStatus: exchange.StatusSubmitted, cancelStatus: exchange.StatusCanceled, exOrderID: "EX-3001"}
	svc := newSvcWithFake(db, fa)

	o, err := svc.Order.PlaceOrder(context.Background(), sid, channelID, &exchange.PlaceOrderReq{
		Market: "spot", Symbol: "BTCUSDT", Side: "buy", Type: "limit",
		Price: "50000", Quantity: "0.01",
	})
	require.NoError(t, err)

	_, err = svc.Order.CancelOrder(context.Background(), sid, channelID, &exchange.CancelOrderReq{OrderID: o.OrderID})
	require.NoError(t, err)

	store := dao.New(db, svcEncKey)
	bs, _ := store.GetBalances(context.Background(), sid, accountID, []string{"USDT"})
	require.Equal(t, "1000", bs[0].Available)
	require.Equal(t, "0", bs[0].Frozen)

	got, _ := store.GetOrder(context.Background(), sid, o.OrderID, "")
	require.Equal(t, int(exchange.StatusCanceled), got.Status)
}
