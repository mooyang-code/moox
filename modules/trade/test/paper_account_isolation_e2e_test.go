package test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/stretchr/testify/require"
)

type isolationAdapters map[string]execution.ExecutionAdapter

func (a isolationAdapters) Adapter(id string) (execution.ExecutionAdapter, error) { return a[id], nil }

func TestPaperAccountIsolationWithProductionDeciderE2E(t *testing.T) {
	for _, stage := range []string{"quote", "refresh"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			s, err := store.Open(filepath.Join(t.TempDir(), "paper-isolation.db"))
			require.NoError(t, err)
			t.Cleanup(func() { _ = s.Close() })
			adapters := isolationAdapters{}
			markets := map[string]*fakeExchange{}
			accounts := map[string]store.TradingAccountRecord{}
			for _, id := range []string{"a", "b"} {
				account := store.TradingAccountRecord{SpaceID: testSpace, TradingAccountID: id, Name: id, Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER", SettlementAsset: "USDT", Status: "ENABLED"}
				require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error { return tx.CreateTradingAccount(account) }))
				account, err = s.GetTradingAccountByID(ctx, id)
				require.NoError(t, err)
				market := newFakeExchange(exchange.MarketTypeSpot)
				markets[id], accounts[id] = market, account
				adapters[id] = &paperexec.Adapter{Store: s, Account: account, MarketData: market, Now: func() time.Time { return testNow }}
			}
			reducer := &consumer.Reducer{Store: s, Now: func() time.Time { return testNow }}
			matcher := &paperexec.Matcher{Store: s, Reducer: reducer, CandidateTimeout: time.Second}
			decider := &paperexec.Decider{Store: s, Adapters: adapters, Now: func() time.Time { return testNow }}
			matcher.DecideContext = decider.Decide
			refreshFailed := stage == "refresh"
			matcher.Refresh = func(ctx context.Context, id string) error {
				if id == "a" && refreshFailed {
					return errors.New("injected account snapshot failure")
				}
				snapshot, err := adapters[id].GetAccountSnapshot(ctx)
				if err != nil {
					return err
				}
				return s.Transaction(ctx, func(tx *store.Tx) error {
					return tx.UpdateTradingAccountFacts(testSpace, id, nil, paperSnapshotRecordForTest(snapshot), testNow.UnixMilli(), testNow.UnixMilli())
				})
			}
			// Exercise real session initialization and readiness, not a fixture
			// that declares all sessions unconditionally ready.
			syncer := &accountsync.Service{Store: s, Adapters: adapters, Fills: reducer, SessionState: readySession(true), Now: func() time.Time { return testNow }}
			sessions := map[string]*traderuntime.ExchangeSession{}
			startSession := func(id string) func() {
				session := &traderuntime.ExchangeSession{Account: accounts[id], Adapter: adapters[id], Sync: syncer, SyncInterval: time.Hour, PaperMatcherReady: func() bool { return true }, PaperAccountState: matcher.AccountState, PaperAccountRecovered: matcher.RecoverAccount}
				sessions[id] = session
				sessionCtx, cancel := context.WithCancel(ctx)
				done := make(chan error, 1)
				go func() { done <- session.Run(sessionCtx) }()
				stopped := false
				stop := func() {
					if stopped {
						return
					}
					stopped = true
					cancel()
					require.ErrorIs(t, <-done, context.Canceled)
				}
				t.Cleanup(stop)
				require.Eventually(t, session.Ready, 2*time.Second, time.Millisecond)
				return stop
			}
			stopA := startSession("a")
			startSession("b")
			require.NoError(t, s.Transaction(ctx, func(tx *store.Tx) error {
				for _, id := range []string{"a", "b"} {
					if err := tx.CreateOrder(store.OrderRecord{SpaceID: testSpace, TradingAccountID: id, OrderID: id, ClientOrderID: id, ExchangeOrderID: id, ExchangeSymbol: testSymbol, InstrumentID: testInstrumentID, OrderType: "MARKET", Side: "BUY", PositionSide: "NET", Quantity: "0.01", ReferencePrice: "50000", ReferencePriceAt: testNow.UnixMilli(), OwnerType: "EXTERNAL", OwnerID: id, State: "OPEN", FirstMatchPending: true, ReservedAsset: "USDT", ReservedQuantity: "501", RemainingReservedQuantity: "501"}); err != nil {
						return err
					}
				}
				return nil
			}))
			if stage == "quote" {
				markets["a"].mu.Lock()
				markets["a"].quoteErr = errors.New("account A quote unavailable")
				markets["a"].mu.Unlock()
			}
			require.NoError(t, matcher.Scan(ctx))
			b, err := s.GetOrder(ctx, testSpace, "b")
			require.NoError(t, err)
			require.Equal(t, "FILLED", b.State)
			require.True(t, sessions["b"].Ready())
			require.False(t, sessions["a"].Ready())
			require.False(t, matcher.AccountState("a").Ready)
			a, err := s.GetOrder(ctx, testSpace, "a")
			require.NoError(t, err)
			if stage == "quote" {
				require.Equal(t, "OPEN", a.State)
				require.Contains(t, matcher.AccountState("a").LastError, "quote")
				markets["a"].mu.Lock()
				markets["a"].quoteErr = nil
				markets["a"].mu.Unlock()
				// A periodic session sync may own the account lock for this scan.
				// Retry as the production matcher worker does, without accepting a
				// failed scan as recovery evidence.
				require.Eventually(t, func() bool {
					require.NoError(t, matcher.Scan(ctx))
					return sessions["a"].Ready()
				}, 3*time.Second, 10*time.Millisecond)
			} else {
				require.Equal(t, "FILLED", a.State)
				require.Contains(t, matcher.AccountState("a").LastError, "refresh")
				refreshFailed = false
				require.NoError(t, matcher.Scan(ctx))
				require.False(t, sessions["a"].Ready(), "an empty scan is not proof that the account recovered")
				stopA()
				startSession("a")
				require.True(t, matcher.AccountState("a").Ready, "successful real session reinitialization clears no-candidate failure")
			}
			_, total, err := s.ListFills(ctx, testSpace, store.FillQuery{Limit: 10})
			require.NoError(t, err)
			require.Equal(t, int64(2), total)
		})
	}
}

type blockingPaperSnapshot struct {
	*paperexec.Adapter
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (a *blockingPaperSnapshot) GetAccountSnapshot(ctx context.Context) (exchange.AccountSnapshot, error) {
	snapshot, err := a.Adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return snapshot, err
	}
	first := false
	a.once.Do(func() { first = true; close(a.entered) })
	if first {
		select {
		case <-a.release:
		case <-ctx.Done():
			return exchange.AccountSnapshot{}, ctx.Err()
		}
	}
	return snapshot, nil
}

func TestPaperSessionInitializationCannotApplySnapshotAcrossConcurrentFillE2E(t *testing.T) {
	f := newProductionPaperFixture(t, exchange.MarketTypeSpot)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	order := mustPlace(t, f, marketSpec("initialization-race", exchange.SideBuy, "0.01"))
	_, err := f.orders.Submit(ctx, testSpace, string(order.ID))
	require.NoError(t, err)
	base := f.adapter.(recordingAdapter).ExecutionAdapter.(*paperexec.Adapter)
	blocked := &blockingPaperSnapshot{Adapter: base, entered: make(chan struct{}), release: make(chan struct{})}
	f.sync.Adapters = adapterSource{adapter: blocked}
	matcher := &paperexec.Matcher{Store: f.store, Reducer: f.reducer, Refresh: func(context.Context, string) error { return errors.New("injected post-fill refresh failure") }}
	matcher.DecideContext = (&paperexec.Decider{Store: f.store, Adapters: adapterSource{adapter: base}, Now: func() time.Time { return testNow }}).Decide
	account, err := f.store.GetTradingAccountByID(ctx, testAccount)
	require.NoError(t, err)
	session := &traderuntime.ExchangeSession{Account: account, Adapter: blocked, Sync: f.sync, SyncInterval: time.Hour, PaperMatcherReady: func() bool { return true }, PaperAccountState: matcher.AccountState, PaperAccountRecovered: matcher.RecoverAccount}
	sessionDone := make(chan error, 1)
	go func() { sessionDone <- session.Run(ctx) }()
	var stopOnce sync.Once
	stopSession := func() { stopOnce.Do(func() { cancel(); require.ErrorIs(t, <-sessionDone, context.Canceled) }) }
	t.Cleanup(stopSession)
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("session did not read its snapshot")
	}
	matchDone := make(chan error, 1)
	go func() { matchDone <- matcher.Scan(ctx) }()
	select {
	case err := <-matchDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		close(blocked.release)
		<-matchDone
		t.Fatal("matching waited behind an account snapshot")
	}
	current, err := f.store.GetOrder(ctx, testSpace, string(order.ID))
	require.NoError(t, err)
	require.Equal(t, "OPEN", current.State, "a busy account must be deferred, not filled across snapshot apply")
	close(blocked.release)
	require.Eventually(t, session.Ready, time.Second, time.Millisecond)
	// The deferred order is retried from facts after snapshot application.
	require.NoError(t, matcher.Scan(ctx))
	state := matcher.AccountState(testAccount)
	require.False(t, state.Ready)
	require.Equal(t, "refresh", state.Stage)
	require.False(t, session.Ready(), "old initialization generation cannot retire the fill's new refresh failure")
	filled, err := f.store.GetOrder(ctx, testSpace, string(order.ID))
	require.NoError(t, err)
	require.Equal(t, "FILLED", filled.State)
	projection, err := f.store.GetPaperBalanceSnapshot(ctx, testSpace, testAccount)
	require.NoError(t, err)
	require.Equal(t, "99500", projection.Totals["USDT"].String())
	require.Equal(t, "0.01", projection.Totals["BTC"].String())
	stopSession()
}
