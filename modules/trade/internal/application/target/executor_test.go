package target

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

type targetOrderServiceStub struct {
	placeErrors  map[string]error
	submitErrors map[string]error
	specs        []orderdomain.OrderSpec
	submitted    []string
	canceled     []string
	discarded    []string
	resolved     []string
}

func (s *targetOrderServiceStub) Place(
	_ context.Context,
	_ string,
	spec orderdomain.OrderSpec,
) (orderdomain.Order, error) {
	s.specs = append(s.specs, spec)
	if err := s.placeErrors[spec.TradingAccountID]; err != nil {
		return orderdomain.Order{}, err
	}
	return orderdomain.Order{
		ID:   shared.OrderID("child-" + spec.TradingAccountID),
		Spec: spec, State: orderdomain.Pending,
	}, nil
}

func (s *targetOrderServiceStub) Submit(
	_ context.Context,
	_ string,
	orderID string,
) (orderdomain.Order, error) {
	s.submitted = append(s.submitted, orderID)
	if err := s.submitErrors[orderID]; err != nil {
		return orderdomain.Order{}, err
	}
	return orderdomain.Order{ID: shared.OrderID(orderID)}, nil
}

func (s *targetOrderServiceStub) Cancel(
	_ context.Context,
	_ string,
	orderID string,
) (orderdomain.Order, error) {
	s.canceled = append(s.canceled, orderID)
	return orderdomain.Order{ID: shared.OrderID(orderID)}, nil
}

func (s *targetOrderServiceStub) DiscardPending(
	_ context.Context,
	_ string,
	orderID string,
) (orderdomain.Order, error) {
	s.discarded = append(s.discarded, orderID)
	return orderdomain.Order{ID: shared.OrderID(orderID)}, nil
}

func (s *targetOrderServiceStub) ResolveUnknown(
	_ context.Context,
	_ string,
	orderID string,
) (orderdomain.Order, error) {
	s.resolved = append(s.resolved, orderID)
	return orderdomain.Order{ID: shared.OrderID(orderID)}, nil
}

type targetPriceStub struct {
	price shared.Decimal
}

func (s targetPriceStub) LatestPrice(
	context.Context,
	string,
	string,
) (Quote, error) {
	return Quote{Price: s.price, UpdatedAt: time.UnixMilli(2_000)}, nil
}

func TestLogicalAccountFullTargetConvergesAcrossBinanceAndOKX(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "1")
	fixture.position(t, "account-b", "BTC-USDT-SWAP", "2")
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "3",
	}})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, fixture.orders.specs)
	target, err := fixture.store.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, StatusConverged, target.Status)
}

func TestTargetExecutorDiscardsPendingChildWhenAutomationChangesBeforeSubmit(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.orders.submitErrors = map[string]error{
		"child-account-a": orderapp.ErrAutomationPaused,
	}
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
	}})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusPaused, result.Status)
	require.Equal(t, "pause", result.Action)
	require.Equal(t, []string{"child-account-a"}, fixture.orders.discarded)
	account, err := fixture.store.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
}

func TestTargetExecutorWaitsWithoutPausingWhenAccountTurnsNotReady(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.orders.submitErrors = map[string]error{
		"child-account-a": orderapp.ErrAccountNotReady,
	}
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
	}})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusPaused, result.Status)
	require.Equal(t, []string{"child-account-a"}, fixture.orders.discarded)
	account, err := fixture.store.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", account.AutomationState)
	target, err := fixture.store.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, StatusPending, target.Status)
}

func TestTargetExecutorPausedOwnerReleaseCancelsOwnedOrderWithoutError(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
	}})
	fixture.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "target-open",
		TradingAccountID: "account-a", ClientOrderID: "target-open",
		Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "BUY", PositionSide: "NET",
		Quantity: "1", ReferencePrice: "100", ReferencePriceAt: 2_000,
		OwnerType: "TARGET", OwnerID: "target-current",
		LogicalAccountID: "logical-1", RunnerID: "runner-1",
		State: "OPEN", Version: 1,
	})
	require.NoError(t, fixture.store.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "PAUSED", "runner ownership released",
		); err != nil {
			return err
		}
		return tx.SetLogicalAccountOwner("space-1", "logical-1", "")
	}))

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusPaused, result.Status)
	require.Equal(t, "cancel", result.Action)
	require.Equal(t, []string{"target-open"}, fixture.orders.canceled)
}

func TestTargetExecutorClosesOpposingBeforeOpening(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "-1")
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "2",
	}})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Len(t, fixture.orders.specs, 1)
	spec := fixture.orders.specs[0]
	require.Equal(t, "account-a", spec.TradingAccountID)
	require.Equal(t, exchange.SideBuy, spec.Side)
	require.Equal(t, "1", spec.Quantity.String())
	require.True(t, spec.ReducePositionOnly)
	require.Len(t, fixture.orders.submitted, 1)
}

func TestTargetExecutorReductionSelectsLargestPosition(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "3")
	fixture.position(t, "account-b", "BTC-USDT-SWAP", "1")
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "2",
	}})

	_, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1)
	spec := fixture.orders.specs[0]
	require.Equal(t, "account-a", spec.TradingAccountID)
	require.Equal(t, exchange.SideSell, spec.Side)
	require.Equal(t, "2", spec.Quantity.String())
	require.True(t, spec.ReducePositionOnly)
}

func TestTargetExecutorIncreaseFallsThroughPriorityOnCapacity(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.orders.placeErrors = map[string]error{
		"account-a": orderapp.ErrInsufficientFunds,
	}
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
	}})

	_, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 2)
	require.Equal(t, "account-a", fixture.orders.specs[0].TradingAccountID)
	require.Equal(t, "account-b", fixture.orders.specs[1].TradingAccountID)
	require.Equal(t, []string{"child-account-b"}, fixture.orders.submitted)
}

func TestTargetExecutorDoesNotRerouteFrozenConversionOnCapacity(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.orders.placeErrors = map[string]error{"account-a": orderapp.ErrInsufficientFunds}
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
		TradingAccountID: "account-a", ExchangeSymbol: "BTCUSDT",
	}})

	result, err := fixture.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, "account-a", fixture.orders.specs[0].TradingAccountID)
	require.Empty(t, fixture.orders.submitted)
	target, err := fixture.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Contains(t, target.BlockedTargets[0].Reason, "frozen target member capacity")
}

func TestTargetExecutorDrainsOtherMembersBeforeFrozenVenue(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-b", "BTC-USDT-SWAP", "1")
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
		TradingAccountID: "account-a", ExchangeSymbol: "BTCUSDT",
	}})

	result, err := fixture.executor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, "account-b", fixture.orders.specs[0].TradingAccountID)
	require.True(t, fixture.orders.specs[0].ReducePositionOnly)
	require.Equal(t, 0, fixture.orders.specs[0].Quantity.Cmp(shared.MustDecimal("1")))
}

func TestTargetExecutorFullOmissionCancelsOldTargetBeforeClosing(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "1")
	fixture.target(t, nil)
	fixture.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "old-child",
		TradingAccountID: "account-a", ClientOrderID: "old-child",
		ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY",
		PositionSide: "NET", Quantity: "1", ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: "target-old",
		LogicalAccountID: "logical-1", RunnerID: "runner-1",
		State: "OPEN", Version: 1,
	})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, []string{"old-child"}, fixture.orders.canceled)
	require.Empty(t, fixture.orders.specs)
}

func TestTargetExecutorPausesOnExternalOrderWithoutCancel(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.target(t, nil)
	fixture.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "external-order",
		TradingAccountID: "account-a", ClientOrderID: "external-order",
		ExchangeOrderID: "exchange-order", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "BUY", PositionSide: "NET",
		Quantity: "1", ReferencePrice: "100",
		OwnerType: "EXTERNAL", OwnerID: "exchange-order",
		LogicalAccountID: "logical-1", State: "OPEN", Version: 1,
	})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusPaused, result.Status)
	logicalAccount, err := fixture.store.GetLogicalAccount(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, "PAUSED", logicalAccount.AutomationState)
	require.Contains(t, logicalAccount.PauseReason, "EXTERNAL")
	require.Empty(t, fixture.orders.canceled)
}

func TestTargetExecutorPausesWhenAnyMemberNotReady(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.setReady(t, "account-b", false)
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "1",
	}})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusPaused, result.Status)
	require.Empty(t, fixture.orders.specs)
}

func TestTargetExecutorRecordsBelowMinimumBlockedTarget(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "0.0001",
	}})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status)
	target, err := fixture.store.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Len(t, target.BlockedTargets, 1)
	require.Contains(t, target.BlockedTargets[0].Reason, "minimum")
	require.Empty(t, fixture.orders.specs)
}

func TestTargetExecutorRecordsBelowMinimumNotionalAsBlocked(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.setMinNotional(t, "BINANCE", "BTCUSDT", "10")
	fixture.setMinNotional(t, "OKX", "BTC-USDT-SWAP", "10")
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SWAP", Quantity: "0.001",
	}})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status)
	target, err := fixture.store.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Len(t, target.BlockedTargets, 1)
	require.Contains(t, target.BlockedTargets[0].Reason, "notional")
	require.Empty(t, fixture.orders.specs)
}

func TestTargetExecutorFullTargetReportsUnmappedExposure(t *testing.T) {
	t.Run("SPOT balance", func(t *testing.T) {
		fixture := newTargetFixture(t, exchange.MarketTypeSpot)
		fixture.setBalance(t, "account-a", "DOGE", "12")
		fixture.target(t, nil)

		result, err := fixture.executor().Converge(
			context.Background(), "space-1", "logical-1",
		)

		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status)
		target, err := fixture.store.GetLogicalAccountTarget(
			context.Background(), "space-1", "logical-1",
		)
		require.NoError(t, err)
		require.Len(t, target.BlockedTargets, 1)
		require.Equal(t, "DOGE", target.BlockedTargets[0].InstrumentID)
		require.Equal(t, "12", target.BlockedTargets[0].Quantity)
		require.Contains(t, target.BlockedTargets[0].Reason, "mapping")
		require.Empty(t, fixture.orders.specs)
	})

	t.Run("SWAP position", func(t *testing.T) {
		fixture := newTargetFixture(t, exchange.MarketTypeSwap)
		fixture.position(t, "account-a", "ETHUSDT", "2")
		fixture.target(t, nil)

		result, err := fixture.executor().Converge(
			context.Background(), "space-1", "logical-1",
		)

		require.NoError(t, err)
		require.Equal(t, StatusBlocked, result.Status)
		target, err := fixture.store.GetLogicalAccountTarget(
			context.Background(), "space-1", "logical-1",
		)
		require.NoError(t, err)
		require.Len(t, target.BlockedTargets, 1)
		require.Equal(t, "ETHUSDT", target.BlockedTargets[0].InstrumentID)
		require.Equal(t, "2", target.BlockedTargets[0].Quantity)
		require.Contains(t, target.BlockedTargets[0].Reason, "mapping")
		require.Empty(t, fixture.orders.specs)
	})
}

func TestTargetExecutorDoesNotTradeWhileExposureIsUnmapped(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSpot)
	fixture.setBalance(t, "account-a", "DOGE", "12")
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SPOT", Quantity: "1",
	}})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status)
	require.Empty(t, fixture.orders.specs)
	target, err := fixture.store.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Len(t, target.BlockedTargets, 1)
	require.Equal(t, "DOGE", target.BlockedTargets[0].InstrumentID)
}

func TestTargetExecutorCancelsCurrentOrderWhenExposureBecomesUnmapped(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSpot)
	fixture.setBalance(t, "account-a", "DOGE", "12")
	fixture.target(t, []store.InstrumentTarget{{
		InstrumentID: "BTC-USDT-SPOT", Quantity: "1",
	}})
	fixture.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "current-child",
		TradingAccountID: "account-a", ClientOrderID: "current-child",
		Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "BUY", Quantity: "1",
		ReferencePrice: "100", ReferencePriceAt: 2_000,
		OwnerType: "TARGET", OwnerID: "target-current",
		LogicalAccountID: "logical-1", RunnerID: "runner-1",
		State: "OPEN", Version: 1,
	})

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status)
	require.Equal(t, "cancel", result.Action)
	require.Equal(t, []string{"current-child"}, fixture.orders.canceled)
	require.Empty(t, fixture.orders.specs)
}

func TestTargetExecutorRecordsBlockedRemainingDelta(t *testing.T) {
	fixture := newTargetFixture(t, exchange.MarketTypeSwap)
	fixture.position(t, "account-a", "BTCUSDT", "0.001")
	fixture.setMinNotional(t, "BINANCE", "BTCUSDT", "10")
	fixture.target(t, nil)

	result, err := fixture.executor().Converge(
		context.Background(), "space-1", "logical-1",
	)

	require.NoError(t, err)
	require.Equal(t, StatusBlocked, result.Status)
	target, err := fixture.store.GetLogicalAccountTarget(
		context.Background(), "space-1", "logical-1",
	)
	require.NoError(t, err)
	require.Len(t, target.BlockedTargets, 1)
	require.Equal(t, "-0.001", target.BlockedTargets[0].Quantity)
	require.Contains(t, target.BlockedTargets[0].Reason, "notional")
}

type targetFixture struct {
	t      *testing.T
	store  *store.Store
	orders *targetOrderServiceStub
	market exchange.MarketType
	now    time.Time
}

func newTargetFixture(t *testing.T, market exchange.MarketType) *targetFixture {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	fixture := &targetFixture{
		t: t, store: tradeStore, orders: &targetOrderServiceStub{},
		market: market, now: time.UnixMilli(2_000).UTC(),
	}
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		for _, account := range []store.TradingAccountRecord{
			fixture.account("account-a", "BINANCE"),
			fixture.account("account-b", "OKX"),
		} {
			if err := tx.CreateTradingAccount(account); err != nil {
				return err
			}
		}
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerRunnerID: "runner-1", ExecutionMode: "PAPER",
			MarketType: string(market), SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		for index, accountID := range []string{"account-a", "account-b"} {
			if err := tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
				SpaceID: "space-1", LogicalAccountID: "logical-1",
				TradingAccountID: accountID, Enabled: true, Priority: index + 1,
			}); err != nil {
				return err
			}
		}
		for _, instrument := range fixture.instruments() {
			if err := tx.UpsertInstrument(instrument); err != nil {
				return err
			}
		}
		return tx.SetLogicalAccountAutomation(
			"space-1", "logical-1", "ACTIVE", "",
		)
	}))
	return fixture
}

func (f *targetFixture) account(
	id string,
	exchangeName string,
) store.TradingAccountRecord {
	return store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: id, Name: id,
		Exchange: exchangeName, MarketType: string(f.market),
		ExecutionMode: "PAPER", Environment: "PAPER",
		SettlementAsset: "USDT", MarginMode: map[bool]string{
			true: "CROSS", false: "",
		}[f.market == exchange.MarketTypeSwap],
		Status: "ENABLED", Ready: true,
		LeverageSettings: store.LeverageSettings{
			"BTCUSDT": "5", "BTC-USDT-SWAP": "5",
		},
		Snapshot: store.TradingAccountSnapshot{
			AvailableFunds: "100000",
			Balances: []store.AssetBalance{{
				Asset: "USDT", Available: "100000", Total: "100000",
			}},
		},
		LastSyncAt: 1_900, SnapshotSourceTime: 1_900,
	}
}

func (f *targetFixture) instruments() []store.InstrumentRecord {
	values := []store.InstrumentRecord{
		{
			Exchange: "BINANCE", MarketType: string(f.market), ExchangeSymbol: "BTCUSDT",
			InstrumentID: "BTC-USDT-" + string(f.market),
			BaseAsset:    "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ExchangeQuantityStep: "1", MinExchangeQuantity: "1",
			PriceTick: "0.1", Status: "TRADING",
		},
		{
			Exchange: "OKX", MarketType: string(f.market), ExchangeSymbol: "BTC-USDT-SWAP",
			InstrumentID: "BTC-USDT-" + string(f.market),
			BaseAsset:    "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
			ExchangeQuantityStep: "1", MinExchangeQuantity: "1",
			PriceTick: "0.1", Status: "TRADING",
		},
	}
	if f.market == exchange.MarketTypeSwap {
		for index := range values {
			values[index].Linear = true
			values[index].ContractValue = "0.001"
			values[index].ContractValueAsset = "BTC"
		}
	}
	return values
}

func (f *targetFixture) target(t *testing.T, targets []store.InstrumentTarget) {
	t.Helper()
	_, accepted, err := f.store.AcceptLogicalAccountTarget(
		context.Background(),
		store.LogicalAccountTargetRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			TargetID: "target-current", RunnerID: "runner-1",
			CommandSequence: 1, Targets: targets, Status: StatusPending,
			AcceptedAt: f.now.UnixMilli(),
		},
	)
	require.NoError(t, err)
	require.True(t, accepted)
}

func (f *targetFixture) position(
	t *testing.T,
	accountID string,
	symbol string,
	quantity string,
) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertPosition(store.PositionRecord{
			SpaceID: "space-1", TradingAccountID: accountID,
			ExchangeSymbol: symbol, PositionSide: "NET", SignedQuantity: quantity,
			EntryPrice: "100", MarkPrice: "100", Leverage: "5",
			MarginMode: "CROSS", ExchangeUpdatedAt: 1_900,
		})
	}))
}

func (f *targetFixture) order(t *testing.T, record store.OrderRecord) {
	t.Helper()
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateOrder(record)
	}))
}

func (f *targetFixture) setReady(t *testing.T, accountID string, ready bool) {
	t.Helper()
	account, err := f.store.GetTradingAccountByID(context.Background(), accountID)
	require.NoError(t, err)
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync(
			account.SpaceID, account.TradingAccountID,
			store.TradingAccountSyncState{
				Ready: ready, Snapshot: account.Snapshot,
				SnapshotSourceTime: account.SnapshotSourceTime,
				LastSyncAt:         f.now.UnixMilli(),
			},
		)
	}))
}

func (f *targetFixture) setBalance(
	t *testing.T,
	accountID string,
	asset string,
	total string,
) {
	t.Helper()
	account, err := f.store.GetTradingAccountByID(context.Background(), accountID)
	require.NoError(t, err)
	account.Snapshot.Balances = append(account.Snapshot.Balances, store.AssetBalance{
		Asset: asset, Available: total, Total: total,
	})
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpdateTradingAccountSync(
			account.SpaceID,
			account.TradingAccountID,
			store.TradingAccountSyncState{
				Ready: account.Ready, Snapshot: account.Snapshot,
				SnapshotSourceTime: account.SnapshotSourceTime,
				LastSyncAt:         account.LastSyncAt,
			},
		)
	}))
}

func (f *targetFixture) setMinNotional(
	t *testing.T,
	exchangeName string,
	symbol string,
	minNotional string,
) {
	t.Helper()
	instrument, err := f.store.GetInstrument(
		context.Background(),
		exchangeName,
		string(f.market),
		symbol,
	)
	require.NoError(t, err)
	instrument.MinNotional = minNotional
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.UpsertInstrument(instrument)
	}))
}

func (f *targetFixture) executor() *Executor {
	return &Executor{
		Store: f.store, Orders: f.orders,
		Prices: targetPriceStub{price: shared.MustDecimal("100")},
		Now:    func() time.Time { return f.now },
	}
}
