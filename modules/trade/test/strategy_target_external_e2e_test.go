//go:build e2e_external

package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	equityapp "github.com/mooyang-code/moox/modules/trade/internal/application/equity"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/eventconsumer"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	paperexec "github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/rpc"
	traderuntime "github.com/mooyang-code/moox/modules/trade/internal/runtime"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestExternalLogicalAccountTargetIsConsumedIntoTradeStore(t *testing.T) {
	natsURL := os.Getenv("MOOX_STRATEGY_TRADE_E2E_NATS_URL")
	u, err := url.Parse(natsURL)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", u.Hostname())
	coord := os.Getenv("MOOX_STRATEGY_TRADE_E2E_COORD_DIR")
	require.NotEmpty(t, coord)
	ctx := spacecontext.WithSpaceID(context.Background(), testSpace)
	path := filepathForTest(t)
	db, err := store.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	logicalSrv := &rpc.LogicalAccountServer{Store: db, LogicalAccounts: &logicalapp.Service{Store: db}}
	console := &rpc.ConsoleServer{
		Store:                db,
		AccountServer:        &rpc.AccountServer{Store: db, Accounts: &accountapp.Service{Store: db}},
		LogicalAccountServer: logicalSrv,
	}
	created, err := console.CreateTradingAccount(ctx, &tradepb.CreateTradingAccountReq{
		Name: "external-paper", Exchange: "binance", Markets: []string{"spot"}, SettlementAsset: "USDT",
		Paper: &tradepb.PaperConfig{InitialBalance: "100000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0"},
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created.GetRetInfo())
	testAccount := created.GetAccount().GetTradingAccountId()
	createdLogical, err := console.CreateLogicalAccount(ctx, &tradepb.CreateLogicalAccountReq{
		Name: "external-strategy", Environment: tradepb.AccountEnvironment_ACCOUNT_ENVIRONMENT_PAPER,
		SettlementAsset: "USDT", ControlMode: tradepb.ControlMode_CONTROL_MODE_STRATEGY,
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, createdLogical.GetRetInfo().GetCode(), createdLogical.GetRetInfo())
	logicalID := createdLogical.GetLogicalAccount().GetLogicalAccountId()
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: testSpace, LogicalAccountID: logicalID, TradingAccountID: testAccount, Enabled: true, Priority: 1,
		})
	}))
	require.NoError(t, os.WriteFile(filepath.Join(coord, "logical-id"), []byte(logicalID), 0600))
	accountRecord, err := db.GetTradingAccount(ctx, testSpace, testAccount)
	require.NoError(t, err)
	fake := newFakeExchange(exchange.MarketTypeSpot)
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpsertInstrument(store.InstrumentRecord{Exchange: "binance", MarketType: "spot", Environment: "PRODUCTION", ExchangeSymbol: testSymbol, InstrumentID: testInstrumentID(exchange.MarketTypeSpot), BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT", ExchangeQuantityStep: "0.001", MinExchangeQuantity: "0.001", PriceTick: "0.1", MinNotional: "5", Status: "TRADING", ExchangeUpdatedAt: testNow.UnixMilli()})
	}))
	adapter := &paperexec.Adapter{Store: db, Account: accountRecord, MarketData: fake, Now: func() time.Time { return testNow }}
	f := buildFixture(db, path, fake, recordingAdapter{ExecutionAdapter: adapter, recorder: fake})
	_, err = f.sync.SyncAccount(ctx, testAccount)
	require.NoError(t, err)
	logical := &logicalapp.Service{Store: f.store, Syncer: syncBridge{service: f.sync}, Now: func() time.Time { return testNow }}
	h := &rpc.LogicalAccountServer{Store: f.store, LogicalAccounts: logical}
	// Trade 不存归属：不认领，直接恢复自动执行，之后按会话先后接受 Strategy 的目标。
	logicalRsp, err := h.GetLogicalAccount(ctx, &tradepb.GetLogicalAccountReq{LogicalAccountId: logicalID})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, logicalRsp.GetRetInfo().GetCode())
	resumed, err := h.ResumeLogicalAccount(ctx, &tradepb.ResumeLogicalAccountReq{LogicalAccountId: logicalID})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, resumed.GetRetInfo().GetCode(), resumed.GetRetInfo())
	prices := targetapp.ExchangePriceSource{Adapters: adapterSource{adapter: f.adapter}}
	equity := &equityapp.Service{Store: f.store, Adapters: adapterSource{adapter: f.adapter}, Now: func() time.Time { return testNow }, SourceMaxAge: time.Minute}
	require.NoError(t, equity.SampleAccount(ctx, testAccount))
	var accountReads atomic.Int32
	// This bridge exercises the real RPC handler and Store across processes;
	// it is intentionally not a claim of production Gateway/tRPC coverage.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/logical-account" || r.URL.Query().Get("space_id") != testSpace {
			http.Error(w, "invalid route or space", http.StatusBadRequest)
			return
		}
		response, callErr := h.GetLogicalAccount(spacecontext.WithSpaceID(r.Context(), testSpace), &tradepb.GetLogicalAccountReq{LogicalAccountId: r.URL.Query().Get("logical_account_id")})
		if callErr != nil {
			http.Error(w, callErr.Error(), http.StatusInternalServerError)
			return
		}
		raw, marshalErr := protojson.Marshal(response)
		if marshalErr != nil {
			http.Error(w, marshalErr.Error(), http.StatusInternalServerError)
			return
		}
		accountReads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	require.NoError(t, os.WriteFile(filepath.Join(coord, "trade-ready"), []byte(server.URL), 0600))
	nc, err := nats.Connect(natsURL)
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, e := js.StreamInfo("MOOX_TRADE"); return e == nil }, 20*time.Second, 50*time.Millisecond)
	consumerCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	client, err := jetstream.Connect(consumerCtx, jetstream.Config{URLs: []string{natsURL}, Name: "strategy-trade-e2e-consumer"})
	require.NoError(t, err)
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		runErr = eventconsumer.RunTarget(consumerCtx, eventconsumer.TargetOptions{Client: client, ConsumerName: "strategy-trade-external-e2e", Store: f.store, WeightResolver: &targetapp.WeightResolver{Store: f.store, Prices: prices, Equity: equity, Now: func() time.Time { return testNow }}})
	}()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-done; client.Close() }) }
	t.Cleanup(stop)
	require.Eventually(t, func() bool {
		current, getErr := f.store.GetLogicalAccountTarget(ctx, testSpace, logicalID)
		return getErr == nil && current.InstanceID == "instance-e2e" && current.SessionID == "session-e2e" && current.SessionStartedAt > 0 && len(current.Targets) == 1 && current.Targets[0].Quantity == "1"
	}, 15*time.Second, 50*time.Millisecond)
	stop()
	require.ErrorIs(t, runErr, context.Canceled)
	require.Positive(t, accountReads.Load(), "Strategy 启用前要只读核对一次组合账户")
	current, err := f.store.GetLogicalAccountTarget(ctx, testSpace, logicalID)
	require.NoError(t, err)
	receipt, err := f.store.GetAcceptedTargetHistory(ctx, testSpace, current.TargetID)
	require.NoError(t, err)
	require.Equal(t, "instance-e2e", receipt.InstanceID)
	require.Equal(t, "session-e2e", receipt.SessionID)
	require.Equal(t, "strategy-e2e", receipt.StrategyID)
	require.Greater(t, receipt.ValidUntil, receipt.BarEndTime)
	require.NotEmpty(t, receipt.RequestHash)
	require.Equal(t, "100000", receipt.Equity)
	require.JSONEq(t, `[{"instrument_id":"BTC-USDT-SPOT","target_weight":"0.5"}]`, receipt.WeightsJSON, "回执只存标准化后带市场后缀的 Trade 内部 ID")
	executor := &targetapp.Executor{Store: f.store, Orders: f.orders, Prices: prices, Now: func() time.Time { return testNow }}
	worker := &traderuntime.TargetWorker{Store: f.store, Executor: executor, Interval: time.Hour, Now: func() time.Time { return testNow }}
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()
	worker.Wake()
	var stopOnce sync.Once
	stopWork := func() { stopOnce.Do(func() { stopWorker(); <-workerDone }) }
	t.Cleanup(stopWork)
	require.Eventually(t, func() bool {
		orders, _, e := f.store.ListOrders(ctx, testSpace, store.OrderQuery{TradingAccountID: testAccount, Limit: 10})
		return e == nil && len(orders) == 1 && orders[0].State == "OPEN"
	}, 5*time.Second, 20*time.Millisecond)
	stopWork()
	productionPaperMatch(t, f)
	execution := &rpc.ExecutionServer{Store: f.store}
	fills, err := execution.ListFills(ctx, &tradepb.ListFillsReq{TradingAccountId: testAccount})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, fills.GetRetInfo().GetCode())
	require.Len(t, fills.GetFills(), 1)
	orders, total, err := f.store.ListOrders(ctx, testSpace, store.OrderQuery{TradingAccountID: testAccount, Limit: 10})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "FILLED", orders[0].State)
	require.Equal(t, "1", orders[0].FilledQuantity)
	account, err := f.store.GetTradingAccount(ctx, testSpace, testAccount)
	require.NoError(t, err)
	balances := map[string]string{}
	for _, b := range account.Snapshot.Balances {
		balances[b.Asset] = b.Total
	}
	require.Equal(t, "1", balances["BTC"])
	require.Equal(t, "50000", balances["USDT"])
	_, err = executor.Converge(ctx, testSpace, logicalID)
	require.NoError(t, err)
	_, total, err = f.store.ListOrders(ctx, testSpace, store.OrderQuery{TradingAccountID: testAccount, Limit: 10})
	require.NoError(t, err)
	require.EqualValues(t, 1, total, "reconvergence must not duplicate the filled target")
	require.NoError(t, f.store.Close())
	reopened, err := store.Open(path)
	require.NoError(t, err)
	defer reopened.Close()
	persistedReceipt, err := reopened.GetAcceptedTargetHistory(ctx, testSpace, current.TargetID)
	require.NoError(t, err)
	require.Equal(t, receipt, persistedReceipt)
	persistedOrder, err := reopened.GetOrder(ctx, testSpace, orders[0].OrderID)
	require.NoError(t, err)
	require.Equal(t, "FILLED", persistedOrder.State)
	_, fillCount, err := reopened.ListFills(ctx, testSpace, store.FillQuery{TradingAccountID: testAccount, Limit: 10})
	require.NoError(t, err)
	require.EqualValues(t, 1, fillCount)
	t.Logf("NATS -> session receipt -> real weight conversion -> TargetWorker -> Paper matcher -> RPC fills: target=%s order=%s fill=%s BTC=%s USDT=%s", current.TargetID, orders[0].OrderID, fills.GetFills()[0].GetFillId(), balances["BTC"], balances["USDT"])
}
