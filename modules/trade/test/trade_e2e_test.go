package test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type scriptedExchange struct {
	placeCalls, queryCalls int
	uncertain              bool
}

func (x *scriptedExchange) Place(_ context.Context, r exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	x.placeCalls++
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
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-1", Status: "OPEN", FilledQuantity: shared.Zero()}, nil
}
func (x *scriptedExchange) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{Version: "r1"}, nil
}
func (x *scriptedExchange) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
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
	in := command.PlaceInput{SpaceID: "space", OrderID: "o1", ClientOrderID: "client-1", AccountID: "account", Symbol: "BTCUSDT", Side: "BUY", Quantity: "2", Price: "10"}
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
	fill := exchange.FillEvent{ExchangeTradeID: "ef1", ExchangeOrderID: "ex-1", ClientOrderID: "client-1", Symbol: "BTCUSDT", Side: "BUY", BaseAsset: "BTC", QuoteAsset: "USDT", Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("10"), Fee: shared.MustDecimal("0.01"), FeeCurrency: "BNB"}
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
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='USDT' AND c_bucket='available'", "-20")
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='BNB' AND c_bucket='available'", "-0.02")
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
