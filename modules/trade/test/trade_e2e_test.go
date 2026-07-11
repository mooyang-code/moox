package test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type scriptedExchange struct {
	placeCalls, queryCalls int
	uncertain              bool
	placeErr               error
	queryErr               error
}

func (x *scriptedExchange) Place(_ context.Context, r exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	x.placeCalls++
	if x.placeErr != nil {
		return exchange.ExchangeOrderResult{}, x.placeErr
	}
	if x.uncertain {
		x.uncertain = false
		return exchange.ExchangeOrderResult{}, &exchange.ClassifiedError{Category: exchange.ErrorTransportUncertain, Err: errors.New("timeout after write")}
	}
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-1", ClientOrderID: r.ClientOrderID, Status: "OPEN", FilledQuantity: shared.Zero()}, nil
}
func (x *scriptedExchange) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}
func (x *scriptedExchange) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	x.queryCalls++
	if x.queryErr != nil {
		err := x.queryErr
		x.queryErr = nil
		return exchange.ExchangeOrderResult{}, err
	}
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-1", Status: "OPEN", FilledQuantity: shared.Zero()}, nil
}
func (x *scriptedExchange) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{Version: "r1"}, nil
}
func (x *scriptedExchange) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}
func (x *scriptedExchange) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}

func TestEventDrivenOrderRecoveryFillLedgerAndProjection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trade.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	x := &scriptedExchange{uncertain: true}
	engine := &command.Engine{Store: s, Adapter: x}
	seed := func(id, asset, amount string) {
		t.Helper()
		err := s.Transaction(ctx, func(tx *store.Tx) error {
			return tx.PostLedger("space", ledger.Transaction{ID: shared.LedgerTransactionID(id), BizType: "deposit", RefType: "deposit", RefID: id, Entries: []ledger.Entry{{AccountID: "exchange-funding", Asset: asset, Bucket: "funding", Amount: shared.MustDecimal(amount).Neg()}, {AccountID: "account", Asset: asset, Bucket: "available", Amount: shared.MustDecimal(amount)}}})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("seed-usdt", "USDT", "100")
	seed("seed-bnb", "BNB", "1")
	in := command.PlaceInput{SpaceID: "space", OrderID: "o1", ClientOrderID: "client-1", AccountID: "account", ChannelID: "channel", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "2", Price: "10"}
	r, err := engine.Place(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(order.Ready) || x.placeCalls != 0 {
		t.Fatalf("intent was not durable-first: %+v calls=%d", r, x.placeCalls)
	}
	dup, err := engine.Place(ctx, in)
	if err != nil || dup.OrderID != "o1" {
		t.Fatalf("idempotency: %+v %v", dup, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	engine.Store = s
	worker := consumer.SubmissionWorker{Engine: engine}
	r, err = worker.Handle(ctx, "space", "o1")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(order.SubmitUnknown) || x.placeCalls != 1 {
		t.Fatalf("want unknown after one submit: %+v calls=%d", r, x.placeCalls)
	}
	r, err = worker.Handle(ctx, "space", "o1")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(order.Open) || x.placeCalls != 1 || x.queryCalls != 1 {
		t.Fatalf("unknown was resubmitted: %+v place=%d query=%d", r, x.placeCalls, x.queryCalls)
	}
	h := consumer.FillHandler{Store: s}
	fill := exchange.FillEvent{ExchangeTradeID: "ef1", ExchangeOrderID: "ex-1", ClientOrderID: "client-1", Symbol: "BTCUSDT", Side: "BUY", BaseAsset: "BTC", QuoteAsset: "USDT", Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("9"), Fee: shared.MustDecimal("0.01"), FeeCurrency: "BNB"}
	if err = h.Handle(ctx, "space", "account", "o1", "f1", fill); err != nil {
		t.Fatal(err)
	}
	if err = h.Handle(ctx, "space", "account", "o1", "f1", fill); err != nil {
		t.Fatal(err)
	}
	fill.ExchangeTradeID = "ef2"
	if err = h.Handle(ctx, "space", "account", "o1", "f2", fill); err != nil {
		t.Fatal(err)
	}
	r, err = s.GetOrder(ctx, "space", "o1")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(order.Filled) || r.FilledQuantity != "2" {
		t.Fatalf("order=%+v", r)
	}
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='BTC' AND c_bucket='available'", "2")
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='USDT' AND c_bucket='available'", "82")
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='USDT' AND c_bucket='frozen'", "0")
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='BNB' AND c_bucket='available'", "0.98")
	assertScalar(t, s, "SELECT c_quantity FROM t_trade_position_projections WHERE c_space_id='space' AND c_account_id='account' AND c_symbol='BTCUSDT'", "2")
	var fills int64
	s.DBForTest().Table("t_trade_fill_events").Count(&fills)
	if fills != 2 {
		t.Fatalf("duplicate fill persisted: %d", fills)
	}
}

func TestFullTargetRebalanceSplitsReversalAndClosesOmittedPositions(t *testing.T) {
	legs, err := (rebalance.Planner{}).BuildMode(rebalance.FullTarget, []rebalance.Target{{Symbol: "ETHUSDT", Quantity: shared.MustDecimal("-5")}}, []rebalance.Current{{Symbol: "ETHUSDT", Quantity: shared.MustDecimal("10")}, {Symbol: "BTCUSDT", Quantity: shared.MustDecimal("2")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(legs) != 3 || !legs[0].ReduceOnly || !legs[1].ReduceOnly || legs[2].ReduceOnly || len(legs[2].DependsOn) != 2 {
		t.Fatalf("unsafe rebalance plan: %+v", legs)
	}
}

func TestPlaceFailsClosedOnInsufficientBalance(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	engine := command.Engine{Store: s, Adapter: &scriptedExchange{}}
	_, err = engine.Place(context.Background(), command.PlaceInput{SpaceID: "s", OrderID: "o", ClientOrderID: "c", AccountID: "a", ChannelID: "ch", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10"})
	if err == nil {
		t.Fatal("order accepted without funds")
	}
	var n int64
	s.DBForTest().Table("t_trade_order_aggregates").Count(&n)
	if n != 0 {
		t.Fatalf("order persisted after failed freeze: %d", n)
	}
}

func TestRejectedOrderReleasesReservation(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("s", ledger.Transaction{ID: "seed", BizType: "deposit", RefType: "deposit", RefID: "seed", Entries: []ledger.Entry{{AccountID: "exchange-funding", Asset: "USDT", Bucket: "funding", Amount: shared.MustDecimal("10").Neg()}, {AccountID: "a", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("10")}}})
	}); err != nil {
		t.Fatal(err)
	}
	fx := &scriptedExchange{placeErr: &exchange.ClassifiedError{Category: exchange.ErrorRejected, Err: errors.New("rejected")}}
	engine := &command.Engine{Store: s, Adapter: fx}
	if _, err = engine.Place(ctx, command.PlaceInput{SpaceID: "s", OrderID: "o", ClientOrderID: "c", AccountID: "a", ChannelID: "ch", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10"}); err != nil {
		t.Fatal(err)
	}
	r, err := (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "s", "o")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "REJECTED" {
		t.Fatalf("state=%s", r.State)
	}
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='s' AND c_account_id='a' AND c_asset='USDT' AND c_bucket='available'", "10")
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='s' AND c_account_id='a' AND c_asset='USDT' AND c_bucket='frozen'", "0")
}

func TestPersistedRebalanceExecutesThroughOrderKernel(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{ID: "seed-btc", BizType: "deposit", RefType: "deposit", RefID: "seed-btc", Entries: []ledger.Entry{{AccountID: "exchange-funding", Asset: "BTC", Bucket: "funding", Amount: shared.MustDecimal("2").Neg()}, {AccountID: "account", Asset: "BTC", Bucket: "available", Amount: shared.MustDecimal("2")}}})
	}); err != nil {
		t.Fatal(err)
	}
	fx := &scriptedExchange{}
	engine := &command.Engine{Store: s, Adapter: fx}
	svc := rebalanceapp.Service{Store: s, Engine: engine}
	err = svc.Create(ctx, rebalanceapp.CreateInput{SpaceID: "space", RunID: "run1", IdempotencyKey: "idem1", AccountID: "account", ChannelID: "channel", MarketSnapshotID: "market1", PositionSnapshotID: "position1", RulesVersion: "rules1", Mode: rebalance.FullTarget, Targets: []rebalance.Target{{Symbol: "BTCUSDT", Quantity: shared.Zero()}}, Currents: []rebalance.Current{{Symbol: "BTCUSDT", Quantity: shared.MustDecimal("2")}}, Markets: map[string]rebalanceapp.Market{"BTCUSDT": {BaseAsset: "BTC", QuoteAsset: "USDT", Price: "10"}}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := svc.Advance(ctx, "space", "run1", "account", "channel")
	if err != nil || status != "EXECUTING" {
		t.Fatalf("advance: %s %v", status, err)
	}
	legs, err := s.ListRebalanceLegs(ctx, "space", "run1")
	if err != nil || len(legs) != 1 {
		t.Fatal(err)
	}
	worker := consumer.SubmissionWorker{Engine: engine}
	if _, err = worker.Handle(ctx, "space", legs[0].PlanID); err != nil {
		t.Fatal(err)
	}
	fill := exchange.FillEvent{ExchangeTradeID: "rebalance-fill", Symbol: "BTCUSDT", Side: "SELL", BaseAsset: "BTC", QuoteAsset: "USDT", Quantity: shared.MustDecimal("2"), Price: shared.MustDecimal("10"), Fee: shared.Zero()}
	if err = (consumer.FillHandler{Store: s}).Handle(ctx, "space", "account", legs[0].PlanID, "rebalance-fill", fill); err != nil {
		t.Fatal(err)
	}
	status, err = svc.Advance(ctx, "space", "run1", "account", "channel")
	if err != nil || status != "COMPLETED" {
		t.Fatalf("completion: %s %v", status, err)
	}
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='BTC' AND c_bucket='frozen'", "0")
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='USDT' AND c_bucket='available'", "20")
}

func TestSubmittingCrashBeforeExchangeCallRetriesSameIntent(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ReconcileBalances(ctx, "space", "account", map[string]map[string]shared.Decimal{"USDT": {"available": shared.MustDecimal("10")}}); err != nil {
		t.Fatal(err)
	}
	fx := &scriptedExchange{queryErr: &exchange.ClassifiedError{Category: exchange.ErrorOrderNotFound, Err: errors.New("order not found")}}
	engine := &command.Engine{Store: s, Adapter: fx}
	if _, err = engine.Place(ctx, command.PlaceInput{SpaceID: "space", OrderID: "o", ClientOrderID: "stable-client-id", AccountID: "account", ChannelID: "channel", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10"}); err != nil {
		t.Fatal(err)
	}
	if err = s.DBForTest().Exec("UPDATE t_trade_order_aggregates SET c_state='SUBMITTING',c_version=c_version+1 WHERE c_space_id='space' AND c_order_id='o'").Error; err != nil {
		t.Fatal(err)
	}
	r, err := (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "space", "o")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != string(order.Open) || fx.queryCalls != 1 || fx.placeCalls != 1 {
		t.Fatalf("recovery=%+v query=%d place=%d", r, fx.queryCalls, fx.placeCalls)
	}
}

func TestBalanceReconciliationZerosAssetsMissingFromExchangeSnapshot(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ReconcileBalances(ctx, "space", "account", map[string]map[string]shared.Decimal{"USDT": {"available": shared.MustDecimal("10")}}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileBalances(ctx, "space", "account", map[string]map[string]shared.Decimal{}); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='USDT' AND c_bucket='available'", "0")
}

func TestCancelReplaceSagaRecoversAndDoesNotResubmitOpenReplacement(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ReconcileBalances(ctx, "space", "account", map[string]map[string]shared.Decimal{"USDT": {"available": shared.MustDecimal("20")}}); err != nil {
		t.Fatal(err)
	}
	fx := &scriptedExchange{}
	engine := &command.Engine{Store: s, Adapter: fx}
	oldInput := command.PlaceInput{SpaceID: "space", OrderID: "old", ClientOrderID: "old-client", AccountID: "account", ChannelID: "channel", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10"}
	if _, err = engine.Place(ctx, oldInput); err != nil {
		t.Fatal(err)
	}
	if _, err = (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "space", "old"); err != nil {
		t.Fatal(err)
	}
	replacement := oldInput
	replacement.OrderID, replacement.ClientOrderID = "new", "new-client"
	payload, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateSaga(store.SagaRecord{SpaceID: "space", SagaID: "saga", Type: "CANCEL_REPLACE", State: "CANCEL_REQUESTED", OrderID: "old", Payload: string(payload), Version: 1})
	}); err != nil {
		t.Fatal(err)
	}
	saga, err := engine.ResumeReplace(ctx, "space", "saga")
	if err != nil || saga.State != "REPLACEMENT_CREATED" {
		t.Fatalf("resume create: %+v %v", saga, err)
	}
	if _, err = (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "space", "new"); err != nil {
		t.Fatal(err)
	}
	saga, err = engine.ResumeReplace(ctx, "space", "saga")
	if err != nil || saga.State != "REPLACEMENT_SUBMITTED" || fx.placeCalls != 2 {
		t.Fatalf("resume submitted: %+v err=%v place=%d", saga, err, fx.placeCalls)
	}
}

func TestContractSellPriceImprovementUsesReservedMargin(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ReconcileBalances(ctx, "space", "account", map[string]map[string]shared.Decimal{"USDT": {"available": shared.MustDecimal("10")}}); err != nil {
		t.Fatal(err)
	}
	engine := &command.Engine{Store: s, Adapter: &scriptedExchange{}}
	input := command.PlaceInput{SpaceID: "space", OrderID: "contract", ClientOrderID: "contract-client", AccountID: "account", ChannelID: "swap-channel", Symbol: "BTCUSDT", MarketType: "swap", BaseAsset: "BTC", QuoteAsset: "USDT", Side: "SELL", Quantity: "1", Price: "10"}
	if _, err = engine.Place(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err = (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "space", "contract"); err != nil {
		t.Fatal(err)
	}
	fill := exchange.FillEvent{ExchangeTradeID: "contract-fill", Symbol: "BTCUSDT", Side: "SELL", BaseAsset: "BTC", QuoteAsset: "USDT", Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("12"), Fee: shared.Zero()}
	if err = (consumer.FillHandler{Store: s}).Handle(ctx, "space", "account", "contract", "contract-fill", fill); err != nil {
		t.Fatal(err)
	}
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='USDT' AND c_bucket='frozen'", "0")
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='USDT' AND c_bucket='margin'", "10")
}

func assertScalar(t *testing.T, s *store.Store, query, want string) {
	t.Helper()
	var got string
	if err := s.DBForTest().Raw(query).Scan(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query %q got %q want %q", query, got, want)
	}
}
