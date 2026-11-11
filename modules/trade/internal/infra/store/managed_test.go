package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedManagedFixture 建一个同时开通现货和合约的账户，并让几类标的各有一笔已成交的订单。
func seedManagedFixture(t *testing.T, fundSettings string, filled []managedOrder) *Store {
	t.Helper()
	s := openTestStore(t)
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		account := testAccount()
		account.Markets = []string{"SPOT", "SWAP"}
		account.MarginMode = "CROSS"
		account.LeverageSettings = LeverageSettings{"*": "5"}
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerInstanceID: "runner-1", OwnerSessionID: "session-1", ExecutionMode: "PAPER",
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
			FundSettingsJSON: fundSettings,
		}); err != nil {
			return err
		}
		if err := tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", TradingAccountID: "account-1", Enabled: true, Priority: 1,
		}); err != nil {
			return err
		}
		for index, order := range filled {
			instrument := InstrumentRecord{
				Exchange: "BINANCE", MarketType: order.market, ExchangeSymbol: order.base + "USDT",
				InstrumentID: order.base + "-USDT-" + order.market, BaseAsset: order.base, QuoteAsset: "USDT",
				SettlementAsset: "USDT", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", Status: "TRADING",
			}
			if order.market == "SWAP" {
				instrument.Linear, instrument.ContractValue, instrument.ContractValueAsset = true, "1", order.base
			}
			if err := tx.UpsertInstrument(instrument); err != nil {
				return err
			}
			id := fmt.Sprintf("order-%d", index)
			runnerID := ""
			if order.owner == "TARGET" {
				runnerID = "runner-1"
			}
			if err := tx.CreateOrder(OrderRecord{
				RunnerID: runnerID,
				SpaceID:  "space-1", OrderID: id, TradingAccountID: "account-1", LogicalAccountID: "logical-1",
				ClientOrderID: id, Exchange: "BINANCE", MarketType: order.market, InstrumentID: instrument.InstrumentID,
				ExchangeSymbol: instrument.ExchangeSymbol, OrderType: "MARKET", Side: "BUY",
				Quantity: "1", FilledQuantity: "1", ReferencePrice: "100",
				OwnerType: order.owner, OwnerID: id, State: "FILLED", Version: 1,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	return s
}

type managedOrder struct{ market, base, owner string }

// 受管资产只看现货标的的成交：BTC 合约成交不会让 BTC 变成受管的现货；系统单不算；用户自持、从没交易过的币不受管。
func TestManagedAssetsOnlyFollowSpotInstrumentsAndMooxOrders(t *testing.T) {
	s := seedManagedFixture(t, "", []managedOrder{
		{"SWAP", "BTC", "TARGET"},   // 合约成交：BTC 现货不受管
		{"SPOT", "ETH", "TARGET"},   // 策略单：受管
		{"SPOT", "SOL", "OPERATOR"}, // 人工单：受管
		{"SPOT", "BNB", "SYSTEM"},   // 系统单（BNB 补买）：不算
	})

	managed, err := s.ManagedAssets(context.Background(), "space-1", "account-1")

	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{"USDT": {}, "ETH": {}, "SOL": {}}, managed)
}

// 纯永续账户只有结算资产受管。
func TestManagedAssetsSwapOnlyAccountIsSettlementAsset(t *testing.T) {
	s := openTestStore(t)
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		account := testAccount()
		account.Markets, account.MarginMode, account.LeverageSettings = []string{"SWAP"}, "CROSS", LeverageSettings{"*": "5"}
		return tx.CreateTradingAccount(account)
	}))

	managed, err := s.ManagedAssets(context.Background(), "space-1", "account-1")

	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{"USDT": {}}, managed)
}

// 排除资产（BNB 保留、保证金币、稳定币保证金、底仓）从受管资产里减掉；结算资产不受影响。
func TestManagedAssetsExcludeFundSettingAssets(t *testing.T) {
	s := seedManagedFixture(t,
		`{"bnb_reserve_amount":"100","margin_coins":{"SOL":"50"},"arbitrage_base_assets":["ETH"],"stablecoin_margin":{"USDC":"0.5","USDT":"0.5"}}`,
		[]managedOrder{
			{"SPOT", "BTC", "TARGET"}, {"SPOT", "ETH", "TARGET"}, {"SPOT", "SOL", "TARGET"},
			{"SPOT", "BNB", "OPERATOR"}, {"SPOT", "USDC", "TARGET"},
		})

	managed, err := s.ManagedAssets(context.Background(), "space-1", "account-1")

	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{"USDT": {}, "BTC": {}}, managed)
}

// 没开 BNB 保留时，BNB 是普通的受管资产。
func TestManagedAssetsKeepBNBWithoutReserve(t *testing.T) {
	s := seedManagedFixture(t, "", []managedOrder{{"SPOT", "BNB", "TARGET"}})

	managed, err := s.ManagedAssets(context.Background(), "space-1", "account-1")

	require.NoError(t, err)
	require.Contains(t, managed, "BNB")
}

// 余额待确认水位按钱包记：抬高、只升不降、按钱包清除，互不影响。
func TestBalancePendingWatermarkIsPerWallet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.CreateTradingAccount(testAccount()) }))

	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		require.NoError(t, tx.RaiseBalancePending("space-1", "account-1", "FUTURES", 2_000))
		require.NoError(t, tx.RaiseBalancePending("space-1", "account-1", "FUTURES", 1_000))
		return tx.RaiseBalancePending("space-1", "account-1", "SPOT", 3_000)
	}))
	account, err := s.GetTradingAccountByID(ctx, "account-1")
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"FUTURES": 2_000, "SPOT": 3_000}, account.BalancePending, "水位只升不降")
	require.True(t, account.HasBalancePending())

	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		require.NoError(t, tx.ClearBalancePendingThrough("space-1", "account-1", "FUTURES", 2_500))
		return tx.ClearBalancePendingThrough("space-1", "account-1", "SPOT", 2_500) // 现货水位更高，保持不变
	}))
	account, err = s.GetTradingAccountByID(ctx, "account-1")
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"SPOT": 3_000}, account.BalancePending)
}

// 资金设置坏了，受管资产照常算（按默认设置），不能让清仓、收敛报错。
func TestManagedAssetsToleratesBrokenFundSettings(t *testing.T) {
	s := seedManagedFixture(t, `{"reserve_ratio":"oops"}`, []managedOrder{{"SPOT", "ETH", "TARGET"}})

	managed, err := s.ManagedAssets(context.Background(), "space-1", "account-1")

	require.NoError(t, err)
	require.Contains(t, managed, "ETH")
}

// 成员被停用后，受管资产仍按所属组合的真实资金设置排除底仓（和清仓同一个入口），不会因为取到默认设置而把底仓当受管现货。
func TestManagedAssetsUseRealSettingsForDisabledMember(t *testing.T) {
	s := seedManagedFixture(t, `{"arbitrage_base_assets":["BTC"]}`, []managedOrder{{"SPOT", "BTC", "TARGET"}, {"SPOT", "ETH", "TARGET"}})
	require.NoError(t, s.DBForTest().Exec(`UPDATE t_logical_account_members SET c_enabled = 0`).Error)

	managed, err := s.ManagedAssets(context.Background(), "space-1", "account-1")

	require.NoError(t, err)
	require.NotContains(t, managed, "BTC", "底仓不受管")
	require.Contains(t, managed, "ETH")
}

// 排除字段坏了：受管资产报错，不能把排除资产当普通资产。
func TestManagedAssetsFailWhenExclusionSettingIsUnreadable(t *testing.T) {
	s := seedManagedFixture(t, `{"arbitrage_base_assets":"BTC"}`, []managedOrder{{"SPOT", "BTC", "TARGET"}})

	_, err := s.ManagedAssets(context.Background(), "space-1", "account-1")

	require.ErrorIs(t, err, ErrFundSettingsUnreadable)
}
