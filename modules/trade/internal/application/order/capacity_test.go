package order

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestCapacityLiveUsesReservationFormulaAndBaseSteps(t *testing.T) {
	for _, tc := range []struct {
		name        string
		market      exchange.MarketType
		side        exchange.Side
		funds, want string
	}{
		{"spot buy fee", exchange.MarketTypeSpot, exchange.SideBuy, "101", "1"},
		{"spot sell base", exchange.MarketTypeSpot, exchange.SideSell, "1", "1"},
		{"swap buy leverage", exchange.MarketTypeSwap, exchange.SideBuy, "20.2", "1"},
		{"swap sell leverage", exchange.MarketTypeSwap, exchange.SideSell, "20.2", "1"},
		{"floor base step", exchange.MarketTypeSwap, exchange.SideBuy, "21", "1"},
		{"below minimum", exchange.MarketTypeSpot, exchange.SideBuy, "1", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newTestServiceForMarket(t, tc.market)
			account := executableAccount(tc.market)
			account.Snapshot.AvailableFunds = shared.MustDecimal(tc.funds)
			for i := range account.Snapshot.Balances {
				account.Snapshot.Balances[i].Available = shared.MustDecimal(tc.funds)
			}
			s.Validator.Accounts = accountEligibilityStub{account: account}
			spec := testSpec(s.now())
			spec.Quantity = shared.MustDecimal("2")
			spec.Side = tc.side
			if tc.market == exchange.MarketTypeSwap {
				spec.PositionSide = exchange.PositionSideNet
			}
			got, err := s.Capacity(context.Background(), "space-1", spec)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.String())
		})
	}
}

type capacityPaperAdapter struct {
	*adapterStub
	now time.Time
}

func (a capacityPaperAdapter) GetQuote(context.Context, shared.ExchangeSymbol) (execution.MarketQuote, error) {
	return execution.MarketQuote{Bid: shared.MustDecimal("100"), Ask: shared.MustDecimal("100"), Last: shared.MustDecimal("90"), SourceTime: a.now}, nil
}
func (a capacityPaperAdapter) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	result := exchange.AccountSnapshot{UsedMargin: shared.MustDecimal("10"), UnrealizedPnL: shared.Zero(), ExchangeUpdatedAt: a.now}
	result.Present.UsedMargin, result.Present.UnrealizedPnL = true, true
	return result, nil
}

func TestCapacityPaperUsesLedgerFeesSlippageAndReservations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		market exchange.MarketType
		side   exchange.Side
		total  string
	}{
		{"spot buy", exchange.MarketTypeSpot, exchange.SideBuy, "103.02"},
		{"spot sell", exchange.MarketTypeSpot, exchange.SideSell, "1"},
		{"swap buy", exchange.MarketTypeSwap, exchange.SideBuy, "32.22"},
		{"swap sell", exchange.MarketTypeSwap, exchange.SideSell, "31.78"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, adapter := newTestServiceForMarket(t, tc.market)
			account := executableAccount(tc.market)
			account.ExecutionMode = exchange.ExecutionModePaper
			account.Paper = &tradingaccount.PaperConfig{InitialBalance: shared.MustDecimal("99999"), MakerFeeRate: shared.MustDecimal("0.02"), TakerFeeRate: shared.MustDecimal("0.01"), SlippageBPS: shared.MustDecimal("100")}
			account.Snapshot = exchange.AccountSnapshot{}
			s.Validator.Accounts = accountEligibilityStub{account: account}
			s.Adapters = errorBoundarySource{adapter: capacityPaperAdapter{adapterStub: adapter, now: s.now()}}
			asset := "USDT"
			if tc.market == exchange.MarketTypeSpot && tc.side == exchange.SideSell {
				asset = "BTC"
			}
			require.NoError(t, db.DBForTest().Exec("UPDATE t_trading_accounts SET c_execution_mode='PAPER',c_live_environment='',c_credential_secret_id='' WHERE c_space_id=? AND c_trading_account_id=?", "space-1", "account-1").Error)
			require.NoError(t, db.Transaction(context.Background(), func(tx *store.Tx) error {
				return tx.CreatePaperAccountConfig(store.PaperAccountConfigRecord{SpaceID: "space-1", TradingAccountID: "account-1", InitialBalance: "99999", MakerFeeRate: "0.02", TakerFeeRate: "0.01", SlippageBPS: "100"})
			}))
			require.NoError(t, db.DBForTest().Exec("INSERT INTO t_paper_asset_balances (c_space_id,c_trading_account_id,c_asset,c_total) VALUES (?,?,?,?) ON CONFLICT (c_space_id,c_trading_account_id,c_asset) DO UPDATE SET c_total=excluded.c_total", "space-1", "account-1", asset, tc.total).Error)
			spec := testSpec(s.now())
			spec.Quantity = shared.MustDecimal("2")
			spec.Side = tc.side
			if tc.market == exchange.MarketTypeSwap {
				spec.PositionSide = exchange.PositionSideNet
			}
			got, err := s.Capacity(context.Background(), "space-1", spec)
			require.NoError(t, err)
			require.Equal(t, "1", got.String())
			spec.Quantity = got
			_, err = s.Place(context.Background(), "space-1", spec)
			require.NoError(t, err)
			got, err = s.Capacity(context.Background(), "space-1", spec)
			require.NoError(t, err)
			require.True(t, got.IsZero())
		})
	}
}

func TestCapacityLiveWatermarkDoesNotDoubleDeductReflectedReservations(t *testing.T) {
	s, db, _ := newTestService(t)
	account := executableAccount(exchange.MarketTypeSpot)
	account.LastSyncAt = time.UnixMilli(200)
	account.Snapshot.Balances[0].Available = shared.MustDecimal("202")
	s.Validator.Accounts = accountEligibilityStub{account: account}
	ctx := context.Background()
	spec := testSpec(s.now())
	validation, err := s.Validator.Validate(ctx, "space-1", spec)
	require.NoError(t, err)
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		for _, item := range []struct {
			id, state string
			submitted int64
		}{{"old", "OPEN", 100}, {"new", "OPEN", 200}} {
			record := store.OrderRecord{SpaceID: "space-1", OrderID: item.id, TradingAccountID: "account-1", ClientOrderID: item.id, Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTC-USDT", OrderType: "MARKET", Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: item.id, State: item.state, Version: 1, ReservedAsset: validation.ReservedAsset, ReservedQuantity: validation.ReservedQuantity.String(), RemainingReservedQuantity: validation.ReservedQuantity.String(), SubmittedAt: item.submitted}
			if err := tx.CreateOrder(record); err != nil {
				return err
			}
		}
		return nil
	}))
	spec.Quantity = shared.MustDecimal("2")
	got, err := s.Capacity(ctx, "space-1", spec)
	require.NoError(t, err)
	require.Equal(t, "1", got.String())
}

func TestCapacityClipsChildNotionalAndNeverPersistsReservation(t *testing.T) {
	s, db, _ := newTestService(t)
	s.Validator.MaxChildNotional = shared.MustDecimal("150")
	spec := testSpec(s.now())
	spec.Quantity = shared.MustDecimal("2.09")
	for i := 0; i < 2; i++ {
		got, err := s.Capacity(context.Background(), "space-1", spec)
		require.NoError(t, err)
		require.Equal(t, "1.5", got.String())
	}
	var count int64
	require.NoError(t, db.DBForTest().Table("t_trade_orders").Count(&count).Error)
	require.Zero(t, count)
}

func TestCapacityLiveLimitUsesLimitPriceWithoutMarketFee(t *testing.T) {
	s, _, _ := newTestService(t)
	account := executableAccount(exchange.MarketTypeSpot)
	account.Snapshot.Balances[0].Available = shared.MustDecimal("110")
	s.Validator.Accounts = accountEligibilityStub{account: account}
	spec := testSpec(s.now())
	spec.Type, spec.FillPolicy, spec.LimitPrice = exchange.OrderTypeLimit, exchange.FillPolicyGTC, decimalPointer("110")
	spec.Quantity = shared.MustDecimal("2")
	got, err := s.Capacity(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.Equal(t, "1", got.String())
}

func TestCapacityLiveIncludesUnsubmittedReservation(t *testing.T) {
	s, _, _ := newTestService(t)
	account := executableAccount(exchange.MarketTypeSpot)
	account.LastSyncAt = s.now().Add(time.Hour)
	account.Snapshot.Balances[0].Available = shared.MustDecimal("202")
	s.Validator.Accounts = accountEligibilityStub{account: account}
	spec := testSpec(s.now())
	_, err := s.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
	spec.Quantity = shared.MustDecimal("2")
	got, err := s.Capacity(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.Equal(t, "1", got.String())
}

func TestCapacityFloorsExactRationalBelowStepBoundary(t *testing.T) {
	s, _, _ := newTestService(t)
	account := executableAccount(exchange.MarketTypeSpot)
	// The quotient is recurring and less than one by much less than 1e-36.
	account.Snapshot.Balances[0].Available = shared.MustDecimal("101").Sub(shared.MustDecimal("0.0000000000000000000000000000000000000001"))
	s.Validator.Accounts = accountEligibilityStub{account: account}
	spec := testSpec(s.now())
	spec.Quantity = shared.MustDecimal("2")
	quantity, err := s.Capacity(context.Background(), "space-1", spec)
	require.NoError(t, err)
	require.Equal(t, "0.9", quantity.String())
	spec.Quantity = quantity
	_, err = s.Place(context.Background(), "space-1", spec)
	require.NoError(t, err)
}

func TestCapacityAccountLockHonorsDeadline(t *testing.T) {
	s, db, _ := newTestService(t)
	unlock := db.LockTradingAccount("account-1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Capacity(ctx, "space-1", testSpec(s.now())); done <- err }()
	select {
	case err := <-done:
		unlock()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		var accountErr *AccountExecutionError
		require.ErrorAs(t, err, &accountErr)
	case <-time.After(time.Second):
		unlock()
		<-done
		t.Fatal("capacity estimate waited for account lock beyond its deadline")
	}
}

func TestCapacityLimitChildCapUsesReferenceNotLimitNotional(t *testing.T) {
	for _, limit := range []string{"80", "120"} {
		t.Run(limit, func(t *testing.T) {
			s, _, _ := newTestService(t)
			s.Validator.MaxChildNotional = shared.MustDecimal("100")
			spec := testSpec(s.now())
			spec.Type, spec.FillPolicy, spec.LimitPrice = exchange.OrderTypeLimit, exchange.FillPolicyGTC, decimalPointer(limit)
			spec.Quantity = shared.MustDecimal("2")
			quantity, err := s.Capacity(context.Background(), "space-1", spec)
			require.NoError(t, err)
			require.Equal(t, "1", quantity.String())
			spec.Quantity = quantity
			_, err = s.Place(context.Background(), "space-1", spec)
			require.NoError(t, err)
		})
	}
}
