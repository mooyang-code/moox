package logicalaccount

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// spotMemberFixture 在 logicalAccountServiceFixture 的组合里加一个启用的现货成员 account-s：
// 有 USDT、BNB（从没被 MooX 交易过，不受管）和 BTC（有过成交的人工单，受管）余额，BTC 数量由参数给出。
func spotMemberFixture(t *testing.T, btc string) (*Service, *store.Store) {
	t.Helper()
	service, tradeStore := logicalAccountServiceFixture(t)
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-s", Name: "现货账户",
			Exchange: "BINANCE", Markets: []string{"SPOT"},
			ExecutionMode: "LIVE", Environment: "TESTNET", CredentialSecretID: "secret-account-s",
			SettlementAsset: "USDT", Status: "ENABLED", Ready: true,
			Snapshot:           spotSnapshot(btc),
			SnapshotSourceTime: 1_900, LastSyncAt: 1_900, LastReadyAt: 1_900,
		}); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(store.InstrumentRecord{
			Exchange: "BINANCE", Environment: "TESTNET", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SPOT", BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ExchangeQuantityStep: "0.0001", MinExchangeQuantity: "0.0001",
			PriceTick: "0.1", MinNotional: "5", Status: "TRADING",
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-s", Enabled: true, Priority: 2,
		}); err != nil {
			return err
		}
		// 有成交的人工单让 BTC 成为受管资产。
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "manual-btc", TradingAccountID: "account-s", ClientOrderID: "manual-btc",
			MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
			Quantity: "0.01", FilledQuantity: "0.01", ReferencePrice: "50000", ReferencePriceAt: 1_000,
			OwnerType: "OPERATOR", OwnerID: "action-btc", LogicalAccountID: "logical-1", State: "FILLED",
		})
	}))
	return service, tradeStore
}

func spotSnapshot(btc string) store.TradingAccountSnapshot {
	return store.TradingAccountSnapshot{
		AvailableFunds: "1000", ExchangeUpdatedAt: 1_900,
		Balances: []store.AssetBalance{
			{Wallet: "SPOT", Asset: "USDT", Available: "1000", Total: "1000"},
			{Wallet: "SPOT", Asset: "BNB", Available: "0.5", Total: "0.5"},
			{Wallet: "SPOT", Asset: "BTC", Available: btc, Total: btc},
		},
	}
}

func disableSpotMember(service *Service) (string, error) {
	return service.AddMember(context.Background(), AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-s", Enabled: false, Priority: 2,
	})
}

func spotMemberEnabled(t *testing.T, tradeStore *store.Store) bool {
	t.Helper()
	members, err := tradeStore.ListLogicalAccountMembers(context.Background(), "space-1", "logical-1", true)
	require.NoError(t, err)
	for _, member := range members {
		if member.TradingAccountID == "account-s" {
			return member.Enabled
		}
	}
	t.Fatal("account-s 不在组合里")
	return false
}

// 停用成员与移出成员同一口径：BNB 这类从没被 MooX 交易过的余额、清仓后卖不掉的零头都不挡停用。
func TestDisableMemberIgnoresUnmanagedBalancesAndDust(t *testing.T) {
	service, tradeStore := spotMemberFixture(t, "0.00001")

	warning, err := disableSpotMember(service)
	require.NoError(t, err)
	require.Empty(t, warning)
	require.False(t, spotMemberEnabled(t, tradeStore))
}

// 受管资产还有能卖的持仓、或还有挂单时仍然不能停用。
func TestDisableMemberRejectsManagedHoldingOrOpenOrder(t *testing.T) {
	service, tradeStore := spotMemberFixture(t, "0.5")
	_, err := disableSpotMember(service)
	require.ErrorIs(t, err, ErrMemberHasExposure)
	require.True(t, spotMemberEnabled(t, tradeStore))

	service, tradeStore = spotMemberFixture(t, "0")
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(store.OrderRecord{
			SpaceID: "space-1", OrderID: "external-open", TradingAccountID: "account-s", ClientOrderID: "external-open",
			ExchangeOrderID: "42", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", OrderType: "LIMIT", Side: "BUY",
			Quantity: "0.01", LimitPrice: stringPointer("40000"), ReferencePrice: "40000", ReferencePriceAt: 1_000,
			OwnerType: "EXTERNAL", OwnerID: "42", State: "OPEN",
		})
	}))
	_, err = disableSpotMember(service)
	require.ErrorIs(t, err, ErrMemberHasExposure)
	require.True(t, spotMemberEnabled(t, tradeStore))
}

// 凭证失效（同步永远不会成功）时和移出成员一样只按本地记录确认：没有受管持仓就停用并提示交易所侧没核对；
// 本地还有受管持仓照样拒绝，并附上同步失败的原因。暂时性的同步失败照旧报错。
func TestDisableMemberWhenCredentialsFailUsesLocalRecords(t *testing.T) {
	service, tradeStore := spotMemberFixture(t, "0.5")
	calls := 0
	service.Syncer = failingAccountSyncer{calls: &calls}
	_, err := disableSpotMember(service)
	require.ErrorContains(t, err, "sync must not run")

	service.Syncer = authFailingSyncer{}
	_, err = disableSpotMember(service)
	require.ErrorIs(t, err, ErrMemberHasExposure)
	require.ErrorContains(t, err, "凭证失效")
	require.True(t, spotMemberEnabled(t, tradeStore))

	// 本地快照过期也不影响：只看本地记录，不要求账户就绪。
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.UpdateTradingAccountSnapshot("space-1", "account-s", spotSnapshot("0.00001")); err != nil {
			return err
		}
		return tx.UpdateTradingAccountReadiness("space-1", "account-s", false, 1_950, "凭证失效")
	}))
	warning, err := disableSpotMember(service)
	require.NoError(t, err)
	require.Equal(t, UnverifiedMemberWarning, warning)
	require.False(t, spotMemberEnabled(t, tradeStore))
}

// 同步成功但账户未就绪时，成员操作报“执行账户还没同步完成”，不能借用恢复自动执行的文案。
func TestMemberChangeOnUnreadyAccountUsesMemberMessage(t *testing.T) {
	service, tradeStore := spotMemberFixture(t, "0")
	setLogicalFixtureReady(t, tradeStore, "account-s", false)

	_, err := disableSpotMember(service)
	require.ErrorIs(t, err, ErrMemberNotSynced)
	require.False(t, errors.Is(err, ErrNotReady))

	_, err = service.RemoveMember(context.Background(), "space-1", "logical-1", "account-s")
	require.ErrorIs(t, err, ErrMemberNotSynced)

	setLogicalFixtureReady(t, tradeStore, "account-b", false)
	_, err = service.AddMember(context.Background(), AddMemberCommand{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-b", Enabled: true, Priority: 3,
	})
	require.ErrorIs(t, err, ErrMemberNotSynced)
}

func stringPointer(value string) *string { return &value }
