package rpc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// 会话未启动、需要接管各有专门的错误码，前端按码判断（重试、提示勾选接管），不再匹配消息文字。
func TestErrorInfoDedicatedTradeCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want tradepb.ErrorCode
	}{
		{"会话未启动", runtime.ErrSessionConfig, tradepb.ErrorCode_TRADE_SESSION_NOT_READY},
		{"包装后的会话未启动", fmt.Errorf("加入组合: %w", runtime.ErrSessionConfig), tradepb.ErrorCode_TRADE_SESSION_NOT_READY},
		{"取执行通道时会话未启动", &orderapp.AccountExecutionError{TradingAccountID: "a", Operation: "adapter", Err: runtime.ErrSessionConfig}, tradepb.ErrorCode_TRADE_SESSION_NOT_READY},
		{"需要接管", logicalapp.ErrAdoptionRequired, tradepb.ErrorCode_TRADE_ADOPTION_REQUIRED},
		{"有持仓不能停用", logicalapp.ErrMemberHasExposure, tradepb.ErrorCode_INVALID_PARAM},
		{"有持仓不能移出", logicalapp.ErrRemovalHasExposure, tradepb.ErrorCode_INVALID_PARAM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := errorInfo(tc.err)
			if info.GetCode() != tc.want {
				t.Fatalf("code = %v, want %v", info.GetCode(), tc.want)
			}
			if info.GetMsg() != tc.err.Error() {
				t.Fatalf("msg = %q", info.GetMsg())
			}
		})
	}
	if tradepb.ErrorCode_TRADE_SESSION_NOT_READY != 19 || tradepb.ErrorCode_TRADE_ADOPTION_REQUIRED != 20 {
		t.Fatal("前端按数字 19、20 判断，编号不能变")
	}
}

type sessionNotReadySyncer struct{}

// SyncAccount 模拟同步时取执行通道失败：和 accountsync 一样把 Manager 返回的 ErrSessionConfig 包进 AccountExecutionError。
func (sessionNotReadySyncer) SyncAccount(_ context.Context, accountID string) error {
	return &orderapp.AccountExecutionError{TradingAccountID: accountID, Operation: "adapter", Err: runtime.ErrSessionConfig}
}

// 从 RPC 入口走一遍：加入组合时会话还没启动，返回码是 TRADE_SESSION_NOT_READY，前端据此重试。
func TestAddLogicalAccountMemberReportsSessionNotReady(t *testing.T) {
	tradeStore := openRPCStore(t)
	handler := &LogicalAccountServer{
		LogicalAccounts: &logicalapp.Service{Store: tradeStore, Syncer: sessionNotReadySyncer{}},
		Store:           tradeStore,
	}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := handler.AddLogicalAccountMember(ctx, &tradepb.AddLogicalAccountMemberReq{
		LogicalAccountId: "logical-1", TradingAccountId: "account-1", Enabled: true, Priority: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_TRADE_SESSION_NOT_READY {
		t.Fatalf("ret_info = %+v", rsp.GetRetInfo())
	}
}

// 移出成员时没有会话、本地还有受管持仓：返回 INVALID_PARAM 并说明有持仓，不能落成“会话还没启动，请稍后重试”（重试永远不会成功）。
func TestRemoveMemberWithoutSessionReportsExposure(t *testing.T) {
	tradeStore := openRPCStore(t)
	service := &logicalapp.Service{Store: tradeStore, Syncer: sessionNotReadySyncer{}}
	ctx := context.Background()
	seedRemovalExposure(t, tradeStore)

	_, err := service.RemoveMember(ctx, "space-1", "logical-1", "account-1")
	info := errorInfo(err)
	if info.GetCode() != tradepb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret_info = %+v", info)
	}
	if !strings.Contains(info.GetMsg(), "持仓") || !strings.Contains(info.GetMsg(), "同步失败：账户会话没有启动") {
		t.Fatalf("msg = %q", info.GetMsg())
	}
}

// seedRemovalExposure 建一个在组合里、本地还有永续持仓的执行账户。
func seedRemovalExposure(t *testing.T, tradeStore *store.Store) {
	t.Helper()
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "account-1",
			Exchange: "BINANCE", Markets: []string{"SWAP"}, ExecutionMode: "LIVE",
			Environment: "TESTNET", CredentialSecretID: "secret-account-1",
			SettlementAsset: "USDT", Status: "ENABLED", MarginMode: "CROSS",
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			ExecutionMode: "LIVE", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", MarketType: "SWAP",
			ExchangeSymbol: "BTCUSDT", PositionSide: "NET", SignedQuantity: "1",
			Leverage: "5", MarginMode: "CROSS", ExchangeUpdatedAt: 1_900,
		})
	}); err != nil {
		t.Fatal(err)
	}
}
