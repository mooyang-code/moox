package rpc

import (
	"context"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/runtime"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/spacecontext"
)

// 订单保留天数：创建模拟账户不传取 5 天，传 0 存 0；更新只有模拟账户能改、能改成 0，越界和实盘账户都拒绝；
// 读取时 PaperConfig 带上当前天数。不传天数创建的纯合约模拟账户确实会按 5 天清理。
func TestPaperOrderRetentionDaysRoundTrip(t *testing.T) {
	tradeStore := openRPCStore(t)
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	accounts := &AccountServer{
		Accounts: &accountapp.Service{
			Store: tradeStore, Secrets: secretSourceStub{}, Inspector: inspectorStub{},
		},
		Store: tradeStore, NewID: func() string { return "live-1" },
	}
	create := func(name string, days *int32) *tradepb.TradingAccount {
		t.Helper()
		rsp, err := accounts.CreateTradingAccount(ctx, &tradepb.CreateTradingAccountReq{
			Name: name, Exchange: "binance",
			Markets:         []string{"swap"},
			SettlementAsset: "USDT", MarginMode: "CROSS",
			Paper: &tradepb.PaperConfig{
				InitialBalance: "1000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0",
				OrderRetentionDays: days,
			},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS {
			t.Fatalf("创建 %s：%v %+v", name, err, rsp.GetRetInfo())
		}
		return rsp.GetAccount()
	}
	days := func(value int32) *int32 { return &value }

	defaulted := create("默认天数", nil)
	if got := defaulted.GetPaper().OrderRetentionDays; got == nil || *got != 5 {
		t.Fatalf("不传天数应取 5，得到 %v", got)
	}
	kept := create("不清理", days(0))
	if got := kept.GetPaper().GetOrderRetentionDays(); got != 0 || kept.GetPaper().OrderRetentionDays == nil {
		t.Fatalf("传 0 应存 0，得到 %v", kept.GetPaper().OrderRetentionDays)
	}
	if rsp, err := accounts.CreateTradingAccount(ctx, &tradepb.CreateTradingAccountReq{
		Name: "越界", Exchange: "binance",
		Markets: []string{"swap"}, SettlementAsset: "USDT",
		Paper: &tradepb.PaperConfig{
			InitialBalance: "1000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0",
			OrderRetentionDays: days(366),
		},
	}); err != nil || rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_INVALID_PARAM {
		t.Fatalf("创建时 366 天应被拒绝：%v %+v", err, rsp.GetRetInfo())
	}

	update := func(id string, value int32) *tradepb.UpdateTradingAccountRsp {
		t.Helper()
		rsp, err := accounts.UpdateTradingAccount(ctx, &tradepb.UpdateTradingAccountReq{TradingAccountId: id, OrderRetentionDays: days(value)})
		if err != nil {
			t.Fatal(err)
		}
		return rsp
	}
	if rsp := update(defaulted.GetTradingAccountId(), 30); rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		rsp.GetAccount().GetPaper().GetOrderRetentionDays() != 30 {
		t.Fatalf("改成 30 天：%+v", rsp)
	}
	if rsp := update(defaulted.GetTradingAccountId(), 0); rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		rsp.GetAccount().GetPaper().OrderRetentionDays == nil || rsp.GetAccount().GetPaper().GetOrderRetentionDays() != 0 {
		t.Fatalf("改成 0 天：%+v", rsp)
	}
	if rsp := update(defaulted.GetTradingAccountId(), -1); rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_INVALID_PARAM {
		t.Fatalf("-1 天应被拒绝：%+v", rsp.GetRetInfo())
	}
	// 不传天数的更新不改天数。
	rsp, err := accounts.GetTradingAccount(ctx, &tradepb.GetTradingAccountReq{TradingAccountId: kept.GetTradingAccountId()})
	if err != nil || rsp.GetAccount().GetPaper().GetOrderRetentionDays() != 0 {
		t.Fatalf("读取不清理的账户：%v %+v", err, rsp)
	}

	live, err := accounts.CreateTradingAccount(ctx, &tradepb.CreateTradingAccountReq{
		Name: "实盘", Exchange: "binance",
		Markets:         []string{"spot"},
		Live:            &tradepb.LiveConfig{Environment: tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET, CredentialSecretId: "secret-1"},
		SettlementAsset: "USDT",
	})
	if err != nil || live.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS {
		t.Fatalf("创建实盘账户：%v %+v", err, live.GetRetInfo())
	}
	if rsp := update("live-1", 5); rsp.GetRetInfo().GetCode() == tradepb.ErrorCode_SUCCESS ||
		rsp.GetRetInfo().GetMsg() != accountapp.ErrOrderRetentionPaperOnly.Error() {
		t.Fatalf("实盘账户改保留天数应被拒绝：%+v", rsp.GetRetInfo())
	}

	// 不传天数创建的纯合约模拟账户按 5 天清理：6 天前结束的订单被删，4 天前的保留。
	second := create("清理验证", nil)
	now := time.Now()
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for id, finished := range map[string]time.Time{"old": now.Add(-6 * 24 * time.Hour), "new": now.Add(-4 * 24 * time.Hour)} {
			if err := tx.CreateOrder(store.OrderRecord{
				SpaceID: "space-1", OrderID: id, TradingAccountID: second.GetTradingAccountId(), ClientOrderID: "client-" + id,
				MarketType: "swap", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
				Quantity: "1", ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: id,
				State: "CANCELED", Version: 1, FinishedAt: finished.UnixMilli(),
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := (&runtime.OrderHistoryPruner{Store: tradeStore}).RunAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	orders, total, err := tradeStore.ListOrders(context.Background(), "space-1", store.OrderQuery{TradingAccountID: second.GetTradingAccountId(), Limit: 10})
	if err != nil || total != 1 || orders[0].OrderID != "new" {
		t.Fatalf("按默认 5 天清理后应只剩 new：%v %d %+v", err, total, orders)
	}
}
