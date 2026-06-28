package dao

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	tradeschema "github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const encKey = "moox-cloud-secret-key-32bytes" // 32 字节 AES-256

func newTestStore(t *testing.T) *GormStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "trade_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(tradeschema.AllSQL()).Error)
	return New(db, encKey)
}

func TestAccountCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const sid = "sp1"
	a := &service.Account{AccountID: "acc1", UserID: "u1", AccountName: "main", AccountType: service.AccountSpot, BaseCurrency: "USDT", Status: service.AccountNormal}
	require.NoError(t, st.CreateAccount(ctx, sid, a))

	got, err := st.GetAccount(ctx, sid, "acc1")
	require.NoError(t, err)
	require.Equal(t, "main", got.AccountName)
	require.Equal(t, service.AccountSpot, got.AccountType)
	require.Equal(t, service.IsDeletedFalse, got.IsDeleted)

	got.AccountName = "main2"
	got.Status = service.AccountFrozen
	require.NoError(t, st.UpdateAccount(ctx, sid, got))
	got2, err := st.GetAccount(ctx, sid, "acc1")
	require.NoError(t, err)
	require.Equal(t, "main2", got2.AccountName)
	require.Equal(t, service.AccountFrozen, got2.Status)

	list, total, err := st.ListAccounts(ctx, sid, service.AccountFilter{UserID: "u1"}, service.Page{PageNo: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, list, 1)

	require.NoError(t, st.DeleteAccount(ctx, sid, "acc1"))
	_, err = st.GetAccount(ctx, sid, "acc1")
	require.ErrorIs(t, err, service.ErrNotFound)
}

func TestBalanceUpsertAndFundFlowAtomicity(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const sid = "sp1"
	require.NoError(t, st.CreateAccount(ctx, sid, &service.Account{AccountID: "acc1", UserID: "u1", AccountType: service.AccountSpot}))

	// 初始 upsert 余额
	require.NoError(t, st.UpsertBalances(ctx, sid, []*service.Balance{
		{AccountID: "acc1", Currency: "USDT", Available: "100", Frozen: "0", Total: "100"},
	}))
	bal, err := st.GetBalances(ctx, sid, "acc1", []string{"USDT"})
	require.NoError(t, err)
	require.Len(t, bal, 1)
	require.Equal(t, "100", bal[0].Total)

	// 追加流水：+50（入金），应原子更新 total=150
	require.NoError(t, st.AppendFundFlows(ctx, sid, []*service.FundFlow{
		{FlowID: "f1", AccountID: "acc1", Currency: "USDT", BizType: "transfer_in", Direction: 1, Amount: "50", RefType: "test", RefID: "r1"},
	}))
	bal, err = st.GetBalances(ctx, sid, "acc1", []string{"USDT"})
	require.NoError(t, err)
	require.Equal(t, "150", bal[0].Total)
	require.Equal(t, "150", bal[0].Available)

	// 流水 balance_after 应回填为 150
	flows, _, err := st.ListFundFlows(ctx, sid, service.FundFlowFilter{AccountID: "acc1"}, service.Page{PageNo: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, flows, 1)
	require.Equal(t, "150", flows[0].BalanceAfter)

	// -30（出金）-> total=120
	require.NoError(t, st.AppendFundFlows(ctx, sid, []*service.FundFlow{
		{FlowID: "f2", AccountID: "acc1", Currency: "USDT", BizType: "transfer_out", Direction: -1, Amount: "30"},
	}))
	bal, _ = st.GetBalances(ctx, sid, "acc1", []string{"USDT"})
	require.Equal(t, "120", bal[0].Total)
}

func TestAPIKeyEncryptDecryptAndMask(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const sid = "sp1"
	require.NoError(t, st.CreateAccount(ctx, sid, &service.Account{AccountID: "acc1", UserID: "u1", AccountType: service.AccountSpot}))

	k := &service.APIKey{
		APIKeyID: "k1", AccountID: "acc1", Exchange: "binance",
		APIKey: "ABCDEFGHIJKLMNOP1234", APISecret: "super-secret-value", Passphrase: "pass",
		PermissionsRaw: []string{"trade", "read"}, Status: 1,
	}
	require.NoError(t, st.CreateAPIKey(ctx, sid, k))

	// List 脱敏：api_key 截断、secret/passphrase 空
	list, err := st.ListAPIKeys(ctx, sid, "acc1")
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Contains(t, list[0].APIKey, "****")
	require.Empty(t, list[0].APISecret)
	require.Empty(t, list[0].Passphrase)
	require.ElementsMatch(t, []string{"trade", "read"}, list[0].PermissionsRaw)

	// Get 解密：返回明文
	got, err := st.GetAPIKey(ctx, sid, "k1")
	require.NoError(t, err)
	require.Equal(t, "ABCDEFGHIJKLMNOP1234", got.APIKey)
	require.Equal(t, "super-secret-value", got.APISecret)
	require.Equal(t, "pass", got.Passphrase)

	// 落库的 secret 应为密文（非明文）
	var row service.APIKey
	require.NoError(t, st.DB().Where("c_api_key_id = ?", "k1").First(&row).Error)
	require.NotEqual(t, "super-secret-value", row.APISecret)

	require.NoError(t, st.DeleteAPIKey(ctx, sid, "k1"))
	_, err = st.GetAPIKey(ctx, sid, "k1")
	require.ErrorIs(t, err, service.ErrNotFound)
}

func TestChannelCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const sid = "sp1"
	require.NoError(t, st.CreateChannel(ctx, sid, &service.TradeChannel{ChannelID: "ch1", ChannelName: "bin spot", Exchange: "binance", MarketType: "spot", AccountID: "acc1"}))
	c, err := st.GetChannel(ctx, sid, "ch1")
	require.NoError(t, err)
	require.Equal(t, "bin spot", c.ChannelName)
	c.ChannelName = "bin spot 2"
	require.NoError(t, st.UpdateChannel(ctx, sid, c))
	c2, _ := st.GetChannel(ctx, sid, "ch1")
	require.Equal(t, "bin spot 2", c2.ChannelName)

	list, total, err := st.ListChannels(ctx, sid, service.ChannelFilter{Exchange: "binance"}, service.Page{PageNo: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, list, 1)

	require.NoError(t, st.DeleteChannel(ctx, sid, "ch1"))
	_, err = st.GetChannel(ctx, sid, "ch1")
	require.ErrorIs(t, err, service.ErrNotFound)
}

func TestOrderSaveUpdateList(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const sid = "sp1"
	o := &service.Order{
		OrderID: "o1", ClientOrderID: "c1", AccountID: "acc1", ChannelID: "ch1",
		Exchange: "binance", Symbol: "BTCUSDT", Side: "buy", OrderType: "limit",
		Price: "50000", Quantity: "0.1", Status: 0,
	}
	require.NoError(t, st.SaveOrder(ctx, sid, o))
	got, err := st.GetOrder(ctx, sid, "o1", "")
	require.NoError(t, err)
	require.Equal(t, "BTCUSDT", got.Symbol)

	got.ExchangeOrderID = "EX1"
	got.Status = 3
	got.FilledQty = "0.1"
	got.AvgPrice = "50000"
	got.FinishedAt = time.Now()
	require.NoError(t, st.UpdateOrder(ctx, sid, got))
	got2, _ := st.GetOrder(ctx, sid, "", "c1")
	require.Equal(t, "EX1", got2.ExchangeOrderID)
	require.Equal(t, 3, got2.Status)

	list, total, err := st.ListOrders(ctx, sid, service.OrderFilter{Symbol: "BTCUSDT"}, service.Page{PageNo: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, list, 1)
}

func TestTradeAppendAndList(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const sid = "sp1"
	require.NoError(t, st.AppendTrades(ctx, sid, []*service.Trade{
		{TradeID: "t1", OrderID: "o1", AccountID: "acc1", ChannelID: "ch1", Exchange: "binance", Symbol: "BTCUSDT", Side: "buy", Price: "50000", Quantity: "0.1", Amount: "5000"},
	}))
	// 幂等：重复 trade_id 不报错且不重复
	require.NoError(t, st.AppendTrades(ctx, sid, []*service.Trade{
		{TradeID: "t1", OrderID: "o1", AccountID: "acc1", Exchange: "binance", Symbol: "BTCUSDT", Side: "buy"},
	}))
	list, total, err := st.ListTrades(ctx, sid, service.TradeFilter{OrderID: "o1"}, service.Page{PageNo: 1, PageSize: 10})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, list, 1)
}

func TestPositionUpsertAndList(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const sid = "sp1"
	require.NoError(t, st.UpsertPositions(ctx, sid, []*service.Position{
		{PositionID: "p1", AccountID: "acc1", ChannelID: "ch1", Exchange: "binance", Symbol: "BTCUSDT", PosSide: "net", Quantity: "0.1", AvgPrice: "50000"},
	}))
	// 同 key 覆盖
	require.NoError(t, st.UpsertPositions(ctx, sid, []*service.Position{
		{PositionID: "p1", AccountID: "acc1", ChannelID: "ch1", Exchange: "binance", Symbol: "BTCUSDT", PosSide: "net", Quantity: "0.2", AvgPrice: "51000"},
	}))
	pos, err := st.ListPositions(ctx, sid, "acc1", "")
	require.NoError(t, err)
	require.Len(t, pos, 1)
	require.Equal(t, "0.2", pos[0].Quantity)
	require.Equal(t, "51000", pos[0].AvgPrice)
}

func TestOrderOperationAppendUpdate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	const sid = "sp1"
	op := &service.OrderOperation{OpID: "op1", AccountID: "acc1", ChannelID: "ch1", OrderID: "o1", OpType: "place", Request: "{}", OpStatus: 0, Operator: "u1"}
	require.NoError(t, st.AppendOrderOperation(ctx, sid, op))
	op.OpStatus = 1
	op.Response = `{"ok":true}`
	op.LatencyMS = 42
	require.NoError(t, st.UpdateOrderOperation(ctx, sid, op))
}
