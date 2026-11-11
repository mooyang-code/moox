package rpc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

const nameTakenText = "名称已被使用，请换一个（已关闭的模拟账户也占用名称）"

func requireNameTaken(t *testing.T, label string, info *tradepb.RetInfo) {
	t.Helper()
	if info.GetCode() != tradepb.ErrorCode_INVALID_PARAM || info.GetMsg() != nameTakenText {
		t.Fatalf("%s：ret_info = %+v", label, info)
	}
}

// 关闭的模拟账户只是停用、名称一直占着：用同名再建模拟账户、实盘账户都回参数错误和“名称已被使用”，不带 SQLite 原文。
func TestClosedPaperSimulationKeepsNameTaken(t *testing.T) {
	tradeStore := openRPCStore(t)
	paper := &papersimulation.Service{Store: tradeStore}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	command := papersimulation.CreateCommand{
		SpaceID: "space-1", AccountName: "模拟一号", LogicalAccountName: "模拟组合",
		Exchange: exchange.ExchangeBinance, Markets: []exchange.MarketType{exchange.MarketTypeSpot},
		SettlementAsset: "USDT", InitialBalance: shared.MustDecimal("1000"),
		MakerFeeRate: shared.Zero(), TakerFeeRate: shared.Zero(), SlippageBPS: shared.Zero(),
	}
	created, err := paper.Create(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if err := paper.Close(ctx, "space-1", created.Account.TradingAccountID); err != nil {
		t.Fatal(err)
	}

	console := &ConsoleServer{Paper: paper}
	for _, item := range []struct{ label, account, logical string }{
		{"账户名和组合名都相同（模拟账户编号相同）", "模拟一号", "模拟组合"},
		{"只有账户名相同", "模拟一号", "另一个组合"},
		{"只有组合名相同", "模拟二号", "模拟组合"},
	} {
		rsp, err := console.CreatePaperSimulation(ctx, &tradepb.CreatePaperSimulationReq{
			AccountName: item.account, LogicalAccountName: item.logical,
			Exchange: tradepb.Exchange_EXCHANGE_BINANCE, Markets: []tradepb.MarketType{tradepb.MarketType_MARKET_TYPE_SPOT},
			SettlementAsset: "USDT", InitialBalance: "1000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0",
		})
		if err != nil {
			t.Fatal(err)
		}
		requireNameTaken(t, item.label, rsp.GetRetInfo())
	}

	accounts := &AccountServer{
		Accounts: &accountapp.Service{
			Store: accountapp.Repository{Store: tradeStore}, Secrets: secretSourceStub{}, Inspector: inspectorStub{},
		},
		Store: tradeStore, NewID: func() string { return "live-1" },
	}
	rsp, err := accounts.CreateTradingAccount(ctx, &tradepb.CreateTradingAccountReq{
		Name: "模拟一号", Exchange: tradepb.Exchange_EXCHANGE_BINANCE,
		Markets:         []tradepb.MarketType{tradepb.MarketType_MARKET_TYPE_SPOT},
		Live:            &tradepb.LiveConfig{Environment: tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET, CredentialSecretId: "secret-1"},
		SettlementAsset: "USDT",
	})
	if err != nil {
		t.Fatal(err)
	}
	requireNameTaken(t, "实盘账户用了关闭的模拟账户的名称", rsp.GetRetInfo())
}

// 执行账户、组合账户改名撞上已有名称，新建组合撞上已有名称：都回参数错误和“名称已被使用”。
func TestRenameAndCreateWithTakenName(t *testing.T) {
	tradeStore := openRPCStore(t)
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	requireNoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for index, name := range []string{"主账户", "副账户"} {
			if err := tx.CreateTradingAccount(store.TradingAccountRecord{
				SpaceID: "space-1", TradingAccountID: fmt.Sprintf("account-%d", index+1), Name: name,
				Exchange: "BINANCE", Markets: []string{"SPOT"}, ExecutionMode: "LIVE",
				Environment: "TESTNET", CredentialSecretID: fmt.Sprintf("secret-%d", index+1),
				SettlementAsset: "USDT", Status: "ENABLED",
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	accounts := &AccountServer{
		Accounts: &accountapp.Service{Store: accountapp.Repository{Store: tradeStore}},
		Store:    tradeStore,
	}
	renamed, err := accounts.UpdateTradingAccount(ctx, &tradepb.UpdateTradingAccountReq{TradingAccountId: "account-2", Name: "主账户"})
	requireNoError(t, err)
	requireNameTaken(t, "执行账户改名", renamed.GetRetInfo())

	ids := []string{"logical-1", "logical-2", "logical-3"}
	logical := &LogicalAccountServer{
		LogicalAccounts: &logicalapp.Service{Store: tradeStore}, Store: tradeStore,
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	for _, name := range []string{"组合甲", "组合乙"} {
		created, err := logical.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{
			Name: name, ExecutionMode: tradepb.ExecutionMode_EXECUTION_MODE_PAPER, SettlementAsset: "USDT",
		})
		requireNoError(t, err)
		if created.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS {
			t.Fatalf("created = %+v", created.GetRetInfo())
		}
	}
	duplicate, err := logical.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{
		Name: "组合甲", ExecutionMode: tradepb.ExecutionMode_EXECUTION_MODE_PAPER, SettlementAsset: "USDT",
	})
	requireNoError(t, err)
	requireNameTaken(t, "新建同名组合", duplicate.GetRetInfo())
	updated, err := logical.UpdateLogicalAccount(ctx, &tradepb.UpdateLogicalAccountReq{LogicalAccountId: "logical-2", Name: "组合甲"})
	requireNoError(t, err)
	requireNameTaken(t, "组合改名", updated.GetRetInfo())
}

// 名称以外的唯一约束仍是冲突，但不再把 SQLite 的约束原文回给前端；版本冲突照旧回原来的说明。
func TestConflictHidesSQLiteConstraintText(t *testing.T) {
	capture := captureErrorLogs(t)
	tradeStore := openRPCStore(t)
	account := store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: "account-1", Name: "主账户",
		Exchange: "BINANCE", Markets: []string{"SPOT"}, ExecutionMode: "LIVE",
		Environment: "TESTNET", CredentialSecretID: "secret-1", SettlementAsset: "USDT", Status: "ENABLED",
	}
	requireNoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error { return tx.CreateTradingAccount(account) }))
	// 同一把密钥的第二个启用账户：撞上密钥唯一索引。
	account.TradingAccountID, account.Name = "account-2", "副账户"
	err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error { return tx.CreateTradingAccount(account) })
	if err == nil || !strings.Contains(err.Error(), "constraint failed") {
		t.Fatalf("err = %v", err)
	}
	info := errorInfo(err)
	if info.GetCode() != tradepb.ErrorCode_CONFLICT || info.GetMsg() != uniqueConflictMessage {
		t.Fatalf("info = %+v", info)
	}

	stale := fmt.Errorf("%w: stale order version", store.ErrConflict)
	if info := errorInfo(stale); info.GetCode() != tradepb.ErrorCode_CONFLICT || info.GetMsg() != stale.Error() {
		t.Fatalf("版本冲突：info = %+v", info)
	}
	if len(capture.errors) != 0 {
		t.Fatalf("冲突不算内部错误，不该写错误日志：%v", capture.errors)
	}
}

// 设置杠杆时执行账户的场所里没有这个合约：参数错误和中文说明，不是“record not found”。
func TestSetLeverageUnknownInstrumentIsInvalidParam(t *testing.T) {
	tradeStore := openRPCStore(t)
	seedRemovalExposure(t, tradeStore)
	handler := &AccountServer{Store: tradeStore}
	rsp, err := handler.SetLeverage(spacecontext.WithSpaceID(context.Background(), "space-1"), &tradepb.SetLeverageReq{
		TradingAccountId: "account-1", InstrumentId: "ETH-USDT-SWAP", Leverage: "3",
	})
	requireNoError(t, err)
	if info := rsp.GetRetInfo(); info.GetCode() != tradepb.ErrorCode_INVALID_PARAM || info.GetMsg() != "这个账户没有该合约" {
		t.Fatalf("ret_info = %+v", info)
	}
}

// 停用成员时账户没有会话：和移出成员一样只按本地记录确认，成功返回并把提示放在返回信息里。
func TestDisableMemberWithoutSessionReturnsWarning(t *testing.T) {
	tradeStore := openRPCStore(t)
	seedRemovalExposure(t, tradeStore)
	requireNoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", MarketType: "SWAP",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "0",
			Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: 2_000,
		})
	}))
	handler := &LogicalAccountServer{
		LogicalAccounts: &logicalapp.Service{Store: tradeStore, Syncer: sessionNotReadySyncer{}},
		Store:           tradeStore,
	}
	rsp, err := handler.AddLogicalAccountMember(spacecontext.WithSpaceID(context.Background(), "space-1"), &tradepb.AddLogicalAccountMemberReq{
		LogicalAccountId: "logical-1", TradingAccountId: "account-1", Enabled: false, Priority: 1,
	})
	requireNoError(t, err)
	info := rsp.GetRetInfo()
	if info.GetCode() != tradepb.ErrorCode_SUCCESS || info.GetMsg() != logicalapp.UnverifiedMemberWarning {
		t.Fatalf("ret_info = %+v", info)
	}
	members := rsp.GetLogicalAccount().GetMembers()
	if len(members) != 1 || members[0].GetEnabled() {
		t.Fatalf("members = %+v", members)
	}
}
