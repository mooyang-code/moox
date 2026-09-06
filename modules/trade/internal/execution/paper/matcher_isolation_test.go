package paper

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func matcherFixture(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "matcher.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.UpsertInstrument(store.InstrumentRecord{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01", ExchangeQuantityStep: "0.001", Status: "TRADING"}); err != nil {
			return err
		}
		for _, id := range []string{"a", "b"} {
			if err := tx.CreateTradingAccount(store.TradingAccountRecord{SpaceID: "s", TradingAccountID: id, Name: id, Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER", SettlementAsset: "USDT", Status: "ENABLED"}); err != nil {
				return err
			}
			if err := tx.CreateOrder(store.OrderRecord{SpaceID: "s", TradingAccountID: id, OrderID: id, ClientOrderID: id, ExchangeOrderID: id, ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: id, State: "OPEN", FirstMatchPending: true}); err != nil {
				return err
			}
		}
		return nil
	}))
	return s
}

func matchFill(o store.OrderRecord) Decision {
	return Decision{Fill: exchange.Fill{ExchangeTradeID: "fill-" + o.OrderID, ExchangeOrderID: o.ExchangeOrderID, ExchangeSymbol: o.ExchangeSymbol, Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("100"), SettlementAsset: "USDT", TradedAt: time.UnixMilli(1000)}}
}

func TestMatcherIsolatesPermanentAccountFailureAndRecovers(t *testing.T) {
	s := matcherFixture(t)
	m := &Matcher{Store: s, Reducer: &consumer.Reducer{Store: s}}
	m.DecideContext = func(_ context.Context, o store.OrderRecord) (Decision, error) {
		if o.TradingAccountID == "a" {
			return Decision{}, errors.New("quote unavailable")
		}
		return matchFill(o), nil
	}
	for range 2 {
		require.NoError(t, m.Scan(context.Background()))
	}
	b, err := s.GetOrder(context.Background(), "s", "b")
	require.NoError(t, err)
	require.Equal(t, "FILLED", b.State)
	require.False(t, m.AccountState("a").Ready)
	require.Contains(t, m.AccountState("a").LastError, "decision")
	require.True(t, m.AccountState("b").Ready)
	m.DecideContext = func(_ context.Context, o store.OrderRecord) (Decision, error) { return matchFill(o), nil }
	require.NoError(t, m.Scan(context.Background()))
	require.True(t, m.AccountState("a").Ready)
}

func TestMatcherCandidateDeadlineCoversDecisionAndRefresh(t *testing.T) {
	for _, stage := range []string{"decision", "refresh"} {
		t.Run(stage, func(t *testing.T) {
			s := matcherFixture(t)
			// The deadline also covers real SQLite reduction. Leave headroom for
			// race instrumentation; blocked callbacks still prove cancellation.
			m := &Matcher{Store: s, Reducer: &consumer.Reducer{Store: s}, CandidateTimeout: 2 * time.Second}
			m.DecideContext = func(ctx context.Context, o store.OrderRecord) (Decision, error) {
				if o.TradingAccountID == "a" && stage == "decision" {
					<-ctx.Done()
					return Decision{}, ctx.Err()
				}
				return matchFill(o), nil
			}
			m.Refresh = func(ctx context.Context, accountID string) error {
				if accountID == "a" && stage == "refresh" {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}
			require.NoError(t, m.Scan(context.Background()))
			b, err := s.GetOrder(context.Background(), "s", "b")
			require.NoError(t, err)
			require.Equal(t, "FILLED", b.State)
			require.Contains(t, m.AccountState("a").LastError, stage)
			// A refresh failure can outlive its filled candidate. Verified account
			// resync must be able to clear it without requiring another order.
			if stage == "refresh" {
				generation := m.AccountState("a").Generation
				require.NoError(t, m.Scan(context.Background()))
				require.False(t, m.AccountState("a").Ready)
				m.RecoverAccount("a", generation)
				require.True(t, m.AccountState("a").Ready)
			}
		})
	}
}

func TestMatcherStopsOnParentCancellationAndSharedStorageFailure(t *testing.T) {
	t.Run("parent cancellation", func(t *testing.T) {
		s := matcherFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		m := &Matcher{Store: s, DecideContext: func(ctx context.Context, _ store.OrderRecord) (Decision, error) {
			calls++
			cancel()
			return Decision{}, ctx.Err()
		}}
		require.ErrorIs(t, m.Scan(ctx), context.Canceled)
		require.Equal(t, 1, calls)
	})
	t.Run("database closed", func(t *testing.T) {
		s := matcherFixture(t)
		require.NoError(t, s.Close())
		require.Error(t, (&Matcher{Store: s}).Scan(context.Background()))
	})
	t.Run("callback database error", func(t *testing.T) {
		s := matcherFixture(t)
		cause := errors.New("database unavailable")
		calls := 0
		m := &Matcher{Store: s, DecideContext: func(context.Context, store.OrderRecord) (Decision, error) {
			calls++
			return Decision{}, InfrastructureError{Err: cause}
		}}
		require.ErrorIs(t, m.Scan(context.Background()), cause)
		require.Equal(t, 1, calls)
	})
}

func TestMatcherStaleCandidateDoesNotStopOtherAccount(t *testing.T) {
	s := matcherFixture(t)
	m := &Matcher{Store: s, Reducer: &consumer.Reducer{Store: s}}
	m.DecideContext = func(ctx context.Context, o store.OrderRecord) (Decision, error) {
		if o.OrderID == "a" {
			require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error { return tx.CancelPaperOrder(o, o.Version, "concurrent cancel") }))
		}
		return matchFill(o), nil
	}
	require.NoError(t, m.Scan(context.Background()))
	b, err := s.GetOrder(context.Background(), "s", "b")
	require.NoError(t, err)
	require.Equal(t, "FILLED", b.State)
}

func TestMatcherBusyAccountDoesNotBlockHealthyAccountOrClearFault(t *testing.T) {
	s := matcherFixture(t)
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		for i := 0; i < 17; i++ {
			id := fmt.Sprintf("a-extra-%02d", i)
			if err := tx.CreateOrder(store.OrderRecord{SpaceID: "s", TradingAccountID: "a", OrderID: id, ClientOrderID: id, ExchangeOrderID: id, ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: "a", State: "OPEN", FirstMatchPending: true}); err != nil {
				return err
			}
		}
		return nil
	}))
	m := &Matcher{Store: s, PageSize: 2, Reducer: &consumer.Reducer{Store: s}, CandidateTimeout: time.Second}
	accountACalls := 0
	m.DecideContext = func(_ context.Context, o store.OrderRecord) (Decision, error) {
		if o.TradingAccountID == "a" {
			accountACalls++
		}
		return matchFill(o), nil
	}
	m.failAccount("a", errors.New("previous quote failure"))
	unlock := s.LockTradingAccount("a")
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	done := make(chan error, 1)
	go func() { done <- m.Scan(context.Background()) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		release()
		<-done
		t.Fatal("busy account held the entire matching scan")
	}
	b, err := s.GetOrder(context.Background(), "s", "b")
	require.NoError(t, err)
	require.Equal(t, "FILLED", b.State)
	require.Equal(t, 1, accountACalls, "remaining busy-account orders must be deferred across pages")
	require.False(t, m.AccountState("a").Ready, "skipping a busy order does not prove recovery")
	release()
	require.NoError(t, m.Scan(context.Background()))
	require.True(t, m.AccountState("a").Ready)
}

func TestMatcherRecoveryDoesNotEraseNewFailure(t *testing.T) {
	m := &Matcher{}
	m.failAccount("a", errors.New("old"))
	generation := m.AccountState("a").Generation
	m.failAccount("a", errors.New("new"))
	m.RecoverAccount("a", generation)
	require.False(t, m.AccountState("a").Ready)
	require.Equal(t, "new", m.AccountState("a").LastError)
}

func TestMatcherRemovesDisabledAccountDiagnostics(t *testing.T) {
	s := matcherFixture(t)
	m := &Matcher{Store: s, Reducer: &consumer.Reducer{Store: s}}
	m.DecideContext = func(_ context.Context, o store.OrderRecord) (Decision, error) { return matchFill(o), nil }
	m.failAccount("a", errors.New("quote failed before closing account"))
	require.NoError(t, s.DBForTest().Exec("UPDATE t_trading_accounts SET c_status='DISABLED' WHERE c_trading_account_id='a'").Error)
	require.NoError(t, m.Scan(context.Background()))
	require.Empty(t, m.AccountErrors())
	require.Empty(t, m.AccountState("a").LastError)
}

func TestMatcherEnqueuesAfterUnlockAndRefresh(t *testing.T) {
	s := matcherFixture(t)
	refreshed := false
	m := &Matcher{Store: s, Reducer: &consumer.Reducer{Store: s}, Refresh: func(context.Context, string) error { refreshed = true; return nil }}
	m.Enqueue = func(id string) {
		require.True(t, refreshed)
		unlock := s.LockTradingAccount(id)
		unlock()
	}
	o, err := s.GetOrder(context.Background(), "s", "a")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- m.MatchOrder(context.Background(), o, matchFill(o)) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Enqueue called while account lock held")
	}
}

func TestPaperSnapshotClassifiesClosedDatabaseAsSharedFailure(t *testing.T) {
	s := matcherFixture(t)
	account, err := s.GetTradingAccountByID(context.Background(), "a")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_, err = (&Adapter{Store: s, Account: account}).GetAccountSnapshot(context.Background())
	var infrastructure InfrastructureError
	require.ErrorAs(t, err, &infrastructure)
}

func TestMatcherReducerConflictIsNotMistakenForStaleCandidate(t *testing.T) {
	s := matcherFixture(t)
	m := &Matcher{Store: s, Reducer: failingMatchReducer{err: store.ErrConflict}, DecideContext: func(_ context.Context, o store.OrderRecord) (Decision, error) { return matchFill(o), nil }}
	require.ErrorIs(t, m.Scan(context.Background()), store.ErrConflict)
}

type failingMatchReducer struct{ err error }

func (r failingMatchReducer) ApplyFillToOrderTx(context.Context, *store.Tx, store.OrderRecord, exchange.Fill, consumer.Source) error {
	return r.err
}

func TestMatcherPagesPastFailedAccountWithoutFixedWindow(t *testing.T) {
	for _, count := range []int{33, 100001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := matcherFixture(t)
			require.NoError(t, s.DBForTest().Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < ?)
INSERT INTO t_trade_orders (c_space_id,c_order_id,c_trading_account_id,c_client_order_id,c_exchange,c_market_type,c_instrument_id,c_exchange_symbol,c_order_type,c_side,c_quantity,c_reference_price,c_reference_price_at,c_owner_type,c_owner_id,c_state,c_first_match_pending)
SELECT 's',printf('a%06d',x),'a',printf('a%06d',x),'BINANCE','SPOT','BTCUSDT','BTCUSDT','MARKET','BUY','1','100',1000,'EXTERNAL','a','OPEN',1 FROM n`, count).Error)
			calls := 0
			m := &Matcher{Store: s, Reducer: &consumer.Reducer{Store: s}, PageSize: 17}
			if count > 1000 {
				m.PageSize = 1024
			}
			m.DecideContext = func(_ context.Context, o store.OrderRecord) (Decision, error) {
				if o.TradingAccountID == "a" {
					calls++
					return Decision{}, errors.New("unavailable")
				}
				return matchFill(o), nil
			}
			require.NoError(t, m.Scan(context.Background()))
			require.Equal(t, 1, calls, "failed account is not retried once per page")
			b, err := s.GetOrder(context.Background(), "s", "b")
			require.NoError(t, err)
			require.Equal(t, "FILLED", b.State)
		})
	}
}

func TestMatcherPrioritizesFirstMatchAcrossPagesWithoutRetryingRestedOrder(t *testing.T) {
	s := matcherFixture(t)
	require.NoError(t, s.DBForTest().Exec("UPDATE t_trade_orders SET c_state='CANCELED'").Error)
	const restingCount = 513
	require.NoError(t, s.DBForTest().Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < ?)
INSERT INTO t_trade_orders (c_space_id,c_order_id,c_trading_account_id,c_client_order_id,c_exchange,c_market_type,c_instrument_id,c_exchange_symbol,c_order_type,c_time_in_force,c_side,c_quantity,c_limit_price,c_reference_price,c_reference_price_at,c_owner_type,c_owner_id,c_state,c_first_match_pending)
SELECT 's',printf('a-rest-%06d',x),'a',printf('a-rest-%06d',x),'BINANCE','SPOT','BTCUSDT','BTCUSDT','LIMIT','GTC','BUY','1','90','100',1000,'EXTERNAL','a','OPEN',0 FROM n`, restingCount).Error)
	require.NoError(t, s.DBForTest().Exec(`INSERT INTO t_trade_orders (c_space_id,c_order_id,c_trading_account_id,c_client_order_id,c_exchange,c_market_type,c_instrument_id,c_exchange_symbol,c_order_type,c_time_in_force,c_side,c_quantity,c_limit_price,c_reference_price,c_reference_price_at,c_owner_type,c_owner_id,c_state,c_first_match_pending)
VALUES ('s','z-first','b','z-first','BINANCE','SPOT','BTCUSDT','BTCUSDT','LIMIT','GTC','BUY','1','90','100',1000,'EXTERNAL','b','OPEN',1)`).Error)

	calls := make([]string, 0, restingCount+1)
	m := &Matcher{Store: s, PageSize: 128, DecideContext: func(_ context.Context, order store.OrderRecord) (Decision, error) {
		calls = append(calls, order.OrderID)
		if !order.FirstMatchPending {
			time.Sleep(time.Millisecond)
		}
		return Decision{Rest: true}, nil
	}}
	require.NoError(t, m.Scan(context.Background()))
	require.Len(t, calls, restingCount+1)
	require.Equal(t, "z-first", calls[0], "new orders awaiting their first match must run before slow resting quotes")
	require.Equal(t, 1, countStrings(calls, "z-first"), "an order moved to resting must not be reconsidered in the same scan")
}

func countStrings(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func TestPaperStorageDeadlineIsInfrastructureFailure(t *testing.T) {
	s := matcherFixture(t)
	account, err := s.GetTradingAccountByID(context.Background(), "a")
	require.NoError(t, err)
	db, err := s.DBForTest().DB()
	require.NoError(t, err)
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = (&Adapter{Store: s, Account: account}).GetAccountSnapshot(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var infrastructure InfrastructureError
	require.ErrorAs(t, err, &infrastructure, "a blocked shared SQLite connection is not an external quote timeout")
}

func TestMatcherAccountDiagnosticIncludesBoundedOrderContext(t *testing.T) {
	m := &Matcher{}
	m.failAccount("account", &CandidateError{Stage: "decision", Err: errors.New(strings.Repeat("x", 1024) + "\n")}, "order")
	state := m.AccountErrors()["account"]
	require.Equal(t, "order", state.OrderID)
	require.Equal(t, "PAPER_DECISION_FAILED", state.ErrorCode)
	require.LessOrEqual(t, len([]rune(state.LastError)), 512)
	require.NotContains(t, state.LastError, "\n")
	m.RecoverAccount("account", state.Generation)
	require.Empty(t, m.AccountErrors())
}

func TestPaperAdapterStoreBoundariesPreserveInfrastructureErrors(t *testing.T) {
	for _, mode := range []string{"closed", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			s := matcherFixture(t)
			account, err := s.GetTradingAccountByID(context.Background(), "a")
			require.NoError(t, err)
			a := &Adapter{Store: s, Account: account}
			_, err = a.GetOrder(context.Background(), "BTCUSDT", "missing")
			require.True(t, exchange.IsKind(err, exchange.ErrorOrderNotFound))
			if mode == "closed" {
				require.NoError(t, s.Close())
			} else {
				db, err := s.DBForTest().DB()
				require.NoError(t, err)
				conn, err := db.Conn(context.Background())
				require.NoError(t, err)
				defer conn.Close()
			}
			for name, call := range map[string]func(context.Context) error{
				"snapshot":  func(ctx context.Context) error { _, err := a.GetAccountSnapshot(ctx); return err },
				"positions": func(ctx context.Context) error { _, err := a.ListPositionSnapshots(ctx); return err },
				"orders":    func(ctx context.Context) error { _, err := a.ListOpenOrders(ctx); return err },
				"fills":     func(ctx context.Context) error { _, _, err := a.ListRecentFills(ctx, "BTCUSDT", ""); return err },
				"order":     func(ctx context.Context) error { _, err := a.GetOrder(ctx, "BTCUSDT", "a"); return err },
			} {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				err := call(ctx)
				cancel()
				var infrastructure InfrastructureError
				require.ErrorAs(t, err, &infrastructure, name)
				if mode == "deadline" {
					require.ErrorIs(t, err, context.DeadlineExceeded, name)
				}
			}
		})
	}
}

type instrumentLoadFailure struct {
	execution.MarketDataSource
	err         error
	quoteCalls  int
	loadCalls   int
	instruments []exchange.Instrument
}

func (a *instrumentLoadFailure) LoadInstruments(context.Context) ([]exchange.Instrument, error) {
	a.loadCalls++
	return a.instruments, a.err
}
func (a *instrumentLoadFailure) GetQuote(context.Context, shared.ExchangeSymbol) (execution.MarketQuote, error) {
	a.quoteCalls++
	return execution.MarketQuote{}, nil
}

func TestPaperNativeSymbolPreservesInstrumentLoadFailure(t *testing.T) {
	cause := errors.New("instrument store unavailable")
	for _, failure := range []error{InfrastructureError{Err: cause}, &InfrastructureError{Err: cause}, fmt.Errorf("wrapped: %w", InfrastructureError{Err: cause})} {
		market := &instrumentLoadFailure{err: failure}
		a := &Adapter{MarketData: market}
		_, err := a.GetReferencePrice(context.Background(), "BTC-USDT")
		require.ErrorIs(t, err, cause)
		require.True(t, IsInfrastructureError(err))
		require.Zero(t, market.quoteCalls, "do not silently use the unmapped canonical symbol after load failure")
	}
	require.False(t, IsInfrastructureError(context.DeadlineExceeded), "external deadline is not storage failure without provenance")
}

func TestPaperPreloadedNativeSymbolDoesNotReloadInstruments(t *testing.T) {
	for _, native := range []string{"BTCUSDT", "BTC-USDT"} {
		t.Run(native, func(t *testing.T) {
			market := &instrumentLoadFailure{instruments: []exchange.Instrument{{InstrumentID: "BTC-USDT-SPOT", ExchangeSymbol: native}}}
			a := &Adapter{MarketData: market}
			_, err := a.LoadInstruments(context.Background())
			require.NoError(t, err)
			market.err = errors.New("instrument endpoint unavailable")
			_, err = a.GetQuote(context.Background(), shared.ExchangeSymbol(native))
			require.NoError(t, err, "known native symbols must not depend on another instrument refresh")
			_, err = a.GetQuote(context.Background(), "BTC-USDT-SPOT")
			require.NoError(t, err)
			require.Equal(t, 1, market.loadCalls)
			require.Equal(t, 2, market.quoteCalls)
			_, err = a.GetQuote(context.Background(), "UNKNOWN")
			require.ErrorIs(t, err, market.err)
		})
	}
}
