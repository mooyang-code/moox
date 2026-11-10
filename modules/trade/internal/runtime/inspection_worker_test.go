package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type recordedPauses struct{ reasons []string }

func (r *recordedPauses) Pause(_ context.Context, _, _ string, reason string) (store.LogicalAccountRecord, error) {
	r.reasons = append(r.reasons, reason)
	return store.LogicalAccountRecord{}, nil
}

type scriptedReinspector struct {
	results map[string]accountapp.ReinspectResult
	err     map[string]error
	calls   []string
}

func (s *scriptedReinspector) Reinspect(_ context.Context, id string) (accountapp.ReinspectResult, error) {
	s.calls = append(s.calls, id)
	return s.results[id], s.err[id]
}

func (s *scriptedReinspector) ReinspectUncounted(ctx context.Context, id string) (accountapp.ReinspectResult, error) {
	return s.Reinspect(ctx, id)
}

// seedInspectionLogical 把 account-1 放进一个启用的组合账户。
func seedInspectionLogical(t *testing.T, tradeStore *store.Store, automation string) {
	t.Helper()
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerInstanceID: "runner-1", OwnerSessionID: "session-1", ExecutionMode: "LIVE",
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "人工暂停",
		}); err != nil {
			return err
		}
		// 加成员要求组合暂停；之后再按需恢复。
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		if automation == "ACTIVE" {
			return tx.SetLogicalAccountAutomation("space-1", "logical-1", "ACTIVE", "")
		}
		return nil
	}))
}

func TestInspectionWorkerPausesLogicalAccountOnBlockersOrModeChange(t *testing.T) {
	for name, result := range map[string]accountapp.ReinspectResult{
		"阻断项":    {Blocked: true, Inspection: exchange.Inspection{Blockers: []exchange.InspectionFinding{{Code: exchange.FindingWithdrawPermission, Message: "密钥带有提现权限"}}}},
		"账户类型变化": {ModeChanged: true},
	} {
		t.Run(name, func(t *testing.T) {
			tradeStore := openRuntimeStore(t)
			seedRuntimeAccount(t, tradeStore)
			seedInspectionLogical(t, tradeStore, "ACTIVE")
			pauser := &recordedPauses{}
			worker := &InspectionWorker{Store: tradeStore, Logical: pauser}
			require.NoError(t, worker.Handle(context.Background(), "space-1", "account-1", result))
			require.Len(t, pauser.reasons, 1)
			require.True(t, store.IsAutoPauseReason(pauser.reasons[0]), "自动暂停才会告警：%s", pauser.reasons[0])
		})
	}
}

func TestInspectionWorkerDoesNotPauseOnTransportFailureOrCleanResult(t *testing.T) {
	tradeStore := openRuntimeStore(t)
	seedRuntimeAccount(t, tradeStore)
	seedInspectionLogical(t, tradeStore, "ACTIVE")
	pauser := &recordedPauses{}
	worker := &InspectionWorker{Store: tradeStore, Logical: pauser}
	require.NoError(t, worker.Handle(context.Background(), "space-1", "account-1", accountapp.ReinspectResult{Inconclusive: true, Cause: errors.New("timeout")}))
	require.NoError(t, worker.Handle(context.Background(), "space-1", "account-1", accountapp.ReinspectResult{}))
	require.Empty(t, pauser.reasons)
}

func TestInspectionWorkerKeepsExistingPauseReason(t *testing.T) {
	tradeStore := openRuntimeStore(t)
	seedRuntimeAccount(t, tradeStore)
	seedInspectionLogical(t, tradeStore, "PAUSED")
	pauser := &recordedPauses{}
	worker := &InspectionWorker{Store: tradeStore, Logical: pauser}
	require.NoError(t, worker.Handle(context.Background(), "space-1", "account-1", accountapp.ReinspectResult{Blocked: true}))
	require.Empty(t, pauser.reasons, "已经暂停的组合不覆盖暂停原因")
}

func TestInspectionWorkerIgnoresAccountOutsideLogicalAccount(t *testing.T) {
	tradeStore := openRuntimeStore(t)
	seedRuntimeAccount(t, tradeStore)
	pauser := &recordedPauses{}
	worker := &InspectionWorker{Store: tradeStore, Logical: pauser}
	require.NoError(t, worker.Handle(context.Background(), "space-1", "account-1", accountapp.ReinspectResult{Blocked: true}))
	require.Empty(t, pauser.reasons)
}

func TestInspectionWorkerChecksOnlyEnabledLiveAccountsAndSkipsBinanceTestnet(t *testing.T) {
	tradeStore := openRuntimeStore(t)
	create := func(id, exchangeName, mode, environment, status string) {
		require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
			return tx.CreateTradingAccount(store.TradingAccountRecord{
				SpaceID: "space-1", TradingAccountID: id, Name: id, Exchange: exchangeName, Markets: []string{"SPOT"},
				ExecutionMode: mode, Environment: environment, CredentialSecretID: "secret-" + id, SettlementAsset: "USDT", Status: status,
			})
		}))
	}
	create("binance-prod", "BINANCE", "LIVE", "PRODUCTION", "ENABLED")
	create("binance-testnet", "BINANCE", "LIVE", "TESTNET", "ENABLED")
	create("okx-testnet", "OKX", "LIVE", "TESTNET", "ENABLED")
	create("binance-disabled", "BINANCE", "LIVE", "PRODUCTION", "DISABLED")
	create("paper", "BINANCE", "PAPER", "PAPER", "ENABLED")
	reinspector := &scriptedReinspector{results: map[string]accountapp.ReinspectResult{}, err: map[string]error{"okx-testnet": errors.New("boom")}}
	worker := &InspectionWorker{Store: tradeStore, Accounts: reinspector, Logical: &recordedPauses{}}
	err := worker.CheckAll(context.Background())
	require.Error(t, err, "单个账户失败要汇总返回")
	require.ElementsMatch(t, []string{"binance-prod", "okx-testnet"}, reinspector.calls, "OKX 模拟盘照常检测，币安测试网和模拟盘不检测")
}

func TestInspectionAttentionReportsBlockersAndFailureStreak(t *testing.T) {
	failing := exchange.Inspection{ConsecutiveFailures: InspectionFailureAlertThreshold - 1, InspectedAt: time.Now().UTC(), CanTradeSpot: true}
	encode := func(value exchange.Inspection) string {
		raw, err := value.Encode()
		require.NoError(t, err)
		return raw
	}
	account := store.TradingAccountRecord{
		Name: "main", Exchange: "BINANCE", Markets: []string{"SPOT"}, ExecutionMode: "LIVE", Environment: "PRODUCTION", Status: "ENABLED",
		InspectionJSON: encode(failing),
	}
	blocked, failingText := InspectionAttention(account)
	require.Empty(t, blocked)
	require.Empty(t, failingText, "连续失败未到 6 次不告警")

	failing.ConsecutiveFailures = InspectionFailureAlertThreshold
	account.InspectionJSON = encode(failing)
	_, failingText = InspectionAttention(account)
	require.Contains(t, failingText, "连续失败 6 次")

	withdraw := exchange.Inspection{AccountMode: exchange.AccountModeBinanceStandard, CanWithdraw: true, CanTradeSpot: true, InspectedAt: time.Now().UTC()}
	account.InspectionJSON = encode(withdraw)
	blocked, _ = InspectionAttention(account)
	require.Contains(t, blocked, "提现权限")
}
