package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/require"
)

// queryUnreflectedReservationsSingle 是拆成两条之前的单条查询，只在测试里当对照：拆开后合计必须与它完全一致。
const queryUnreflectedReservationsSingle = `SELECT c_remaining_reserved_quantity
	FROM t_trade_orders
	WHERE c_space_id = ? AND c_trading_account_id = ?
		AND c_reserved_asset = ?
		AND c_market_type IN (SELECT value FROM json_each(?))
		AND c_remaining_reserved_quantity != '0'
		AND (
			c_state IN ('PENDING', 'SUBMITTING', 'SUBMIT_UNKNOWN')
			OR c_submitted_at >= ?
		)`

// 拆成两条查询（还没提交完的、快照之后提交的）后，合计与原来的单条查询逐组一致：同时满足两个条件的单只算一次，
// 别的账户、别的资产、别的市场、预留已用完的单都不算。
func TestGetUnreflectedReservationMatchesSingleQuery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		for _, accountID := range []string{"account-1", "account-2"} {
			account := testAccount()
			account.TradingAccountID, account.Name, account.Markets = accountID, accountID, []string{"SPOT", "SWAP"}
			if err := tx.CreateTradingAccount(account); err != nil {
				return err
			}
		}
		index := 0
		for _, accountID := range []string{"account-1", "account-2"} {
			for _, market := range []string{"SPOT", "SWAP"} {
				for _, asset := range []string{"USDT", "BTC"} {
					for _, state := range []string{"PENDING", "SUBMITTING", "SUBMIT_UNKNOWN", "OPEN", "PARTIALLY_FILLED", "CANCELING"} {
						for _, submittedAt := range []int64{0, 100, 200, 300} {
							for _, reserved := range []string{"0", "7"} {
								index++
								id := fmt.Sprintf("order-%d", index)
								// 每笔数量不同（7 + 序号），重复计数或漏算都会让合计对不上。
								quantity := reserved
								if reserved != "0" {
									quantity = fmt.Sprintf("%d", 7+index)
								}
								if err := tx.CreateOrder(OrderRecord{
									SpaceID: "space-1", OrderID: id, TradingAccountID: accountID,
									ClientOrderID: id, Exchange: "BINANCE", MarketType: market,
									ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
									Quantity: "1", ReferencePrice: "100",
									OwnerType: "EXTERNAL", OwnerID: id,
									State: state, Version: 1, ReservedAsset: asset,
									ReservedQuantity: quantity, RemainingReservedQuantity: quantity,
									SubmittedAt: submittedAt,
								}); err != nil {
									return err
								}
							}
						}
					}
				}
			}
		}
		return nil
	}))
	checked := 0
	for _, markets := range [][]string{{"SPOT"}, {"SWAP"}, {"SPOT", "SWAP"}} {
		for _, asset := range []string{"USDT", "BTC"} {
			for _, snapshot := range []int64{0, 1, 100, 150, 200, 300, 301} {
				marketsJSON := `["` + markets[0] + `"]`
				if len(markets) == 2 {
					marketsJSON = `["SPOT","SWAP"]`
				}
				var rows []string
				require.NoError(t, s.DBForTest().Raw(queryUnreflectedReservationsSingle,
					"space-1", "account-1", asset, marketsJSON, snapshot).Scan(&rows).Error)
				want := shared.Zero()
				for _, row := range rows {
					want = want.Add(shared.MustDecimal(row))
				}
				require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
					got, err := tx.GetUnreflectedReservation("space-1", "account-1", asset, markets, snapshot)
					require.NoError(t, err)
					require.Equal(t, want.String(), got.String(), "markets=%v asset=%s snapshot=%d", markets, asset, snapshot)
					return nil
				}))
				if !want.IsZero() {
					checked++
				}
			}
		}
	}
	require.Positive(t, checked)
}

func TestGetUnreflectedReservationUsesOrderFactsWithoutLedger(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-SPOT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettlementAsset: "USDT", ExchangeQuantityStep: "0.0001",
			MinExchangeQuantity: "0.0001", PriceTick: "0.01", Status: "TRADING",
		}); err != nil {
			return err
		}
		for index, current := range []struct {
			state       string
			submittedAt int64
			reserved    string
		}{
			{state: "OPEN", submittedAt: 100, reserved: "40"},
			{state: "OPEN", submittedAt: 200, reserved: "20"},
			{state: "OPEN", submittedAt: 300, reserved: "50"},
			{state: "PENDING", reserved: "30"},
		} {
			id := fmt.Sprintf("order-%d", index)
			if err := tx.CreateOrder(OrderRecord{
				SpaceID: "space-1", OrderID: id, TradingAccountID: "account-1",
				ClientOrderID: id, Exchange: "BINANCE", MarketType: "SPOT",
				ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
				Quantity: "1", ReferencePrice: "100",
				OwnerType: "EXTERNAL", OwnerID: id,
				State: current.state, Version: 1, ReservedAsset: "USDT",
				ReservedQuantity:          current.reserved,
				RemainingReservedQuantity: current.reserved,
				SubmittedAt:               current.submittedAt,
			}); err != nil {
				return err
			}
		}
		total, err := tx.GetUnreflectedReservation(
			"space-1",
			"account-1",
			"USDT",
			[]string{"SPOT"},
			200,
		)
		require.NoError(t, err)
		require.Equal(t, "100", total.String())
		// 合约钱包的预留不占用现货钱包的资金。
		swapTotal, err := tx.GetUnreflectedReservation("space-1", "account-1", "USDT", []string{"SWAP"}, 200)
		require.NoError(t, err)
		require.True(t, swapTotal.IsZero())
		return nil
	}))
}
