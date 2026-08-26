package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

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
			200,
		)
		require.NoError(t, err)
		require.Equal(t, "100", total.String())
		return nil
	}))
}
