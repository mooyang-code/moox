package test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type replaceAdapter struct{ scriptedExchange }

func (replaceAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}

type countingExchange struct {
	scriptedExchange
	cancelCalls int
}

func (c *countingExchange) Cancel(_ context.Context, _, _ string) (exchange.ExchangeOrderResult, error) {
	c.cancelCalls++
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}

func TestCancelOpenOrderReleasesFrozenBalance(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed"), BizType: "deposit", RefType: "deposit", RefID: "seed",
			Entries: []ledger.Entry{
				{AccountID: "funding", Asset: "USDT", Bucket: "funding", Amount: shared.MustDecimal("50").Neg()},
				{AccountID: "account", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("50")},
			},
		})
	}); err != nil {
		t.Fatal(err)
	}
	fx := &countingExchange{}
	engine := &command.Engine{Store: s, Adapter: fx}
	in := command.PlaceInput{
		SpaceID: "space", OrderID: "cancel-me", ClientOrderID: "cancel-cli",
		AccountID: "account", ChannelID: "channel", Symbol: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10",
	}
	if _, err = engine.Place(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err = (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "space", "cancel-me"); err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Cancel(ctx, "space", "cancel-me"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOrder(ctx, "space", "cancel-me")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != string(order.Canceled) {
		t.Fatalf("state=%s", got.State)
	}
	assertScalar(t, s, "SELECT c_amount FROM t_trade_balance_projections WHERE c_space_id='space' AND c_account_id='account' AND c_asset='USDT' AND c_bucket='frozen'", "0")
}

func TestPartialFillLeavesOrderPartiallyFilled(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ReconcileBalances(ctx, "space", "account", map[string]map[string]shared.Decimal{
		"USDT": {"available": shared.MustDecimal("100")},
	}); err != nil {
		t.Fatal(err)
	}
	engine := &command.Engine{Store: s, Adapter: &scriptedExchange{}}
	in := command.PlaceInput{
		SpaceID: "space", OrderID: "partial", ClientOrderID: "partial-cli",
		AccountID: "account", ChannelID: "channel", Symbol: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "2", Price: "10",
	}
	if _, err = engine.Place(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err = (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "space", "partial"); err != nil {
		t.Fatal(err)
	}
	fill := exchange.FillEvent{
		ExchangeTradeID: "pf-1", Symbol: "BTCUSDT", Side: "BUY",
		BaseAsset: "BTC", QuoteAsset: "USDT",
		Quantity: shared.MustDecimal("1"), Price: shared.MustDecimal("10"), Fee: shared.Zero(),
	}
	if _, err = (consumer.FillHandler{Store: s}).HandleSource(ctx, "space", "account", "partial", "pf-1", fill, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOrder(ctx, "space", "partial")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != string(order.PartiallyFilled) || got.FilledQuantity != "1" {
		t.Fatalf("order=%+v", got)
	}
}

func TestAdvanceReplaceSagaEndToEnd(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ReconcileBalances(ctx, "space", "account", map[string]map[string]shared.Decimal{
		"USDT": {"available": shared.MustDecimal("100")},
	}); err != nil {
		t.Fatal(err)
	}
	engine := &command.Engine{Store: s, Adapter: &replaceAdapter{}}
	oldInput := command.PlaceInput{
		SpaceID: "space", OrderID: "old-r", ClientOrderID: "old-r-cli",
		AccountID: "account", ChannelID: "channel", Symbol: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10",
	}
	if _, err = engine.Place(ctx, oldInput); err != nil {
		t.Fatal(err)
	}
	if _, err = (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "space", "old-r"); err != nil {
		t.Fatal(err)
	}
	replacement := oldInput
	replacement.OrderID, replacement.ClientOrderID = "new-r", "new-r-cli"
	saga, err := engine.Replace(ctx, "saga-e2e", "old-r", replacement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Submit(ctx, "space", saga.ReplacementOrderID, ""); err != nil {
		t.Fatal(err)
	}
	if saga, err = engine.AdvanceReplace(ctx, "space", "saga-e2e"); err != nil {
		t.Fatal(err)
	}
	if saga.State != "REPLACEMENT_SUBMITTED" {
		t.Fatalf("saga=%+v", saga)
	}
}

func TestDeltaTargetRebalanceCreatesBuyLeg(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.PostLedger("space", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed-usdt"), BizType: "deposit", RefType: "deposit", RefID: "seed",
			Entries: []ledger.Entry{
				{AccountID: "funding", Asset: "USDT", Bucket: "funding", Amount: shared.MustDecimal("100000").Neg()},
				{AccountID: "account", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("100000")},
			},
		})
	}); err != nil {
		t.Fatal(err)
	}
	svc := rebalanceapp.Service{Store: s, Engine: &command.Engine{Store: s, Adapter: &scriptedExchange{}}}
	if err = svc.Create(ctx, rebalanceapp.CreateInput{
		SpaceID: "space", RunID: "delta-1", IdempotencyKey: "delta-idem", AccountID: "account", ChannelID: "channel",
		MarketSnapshotID: "m1", PositionSnapshotID: "p1", RulesVersion: "r1", Mode: rebalance.PatchTarget,
		Targets:  []rebalance.Target{{Symbol: "BTCUSDT", Quantity: shared.MustDecimal("1")}},
		Currents: []rebalance.Current{{Symbol: "BTCUSDT", Quantity: shared.Zero()}},
		Markets:  map[string]rebalanceapp.Market{"BTCUSDT": {BaseAsset: "BTC", QuoteAsset: "USDT", Price: "50000"}},
	}); err != nil {
		t.Fatal(err)
	}
	status, err := svc.Advance(ctx, "space", "delta-1", "account", "channel")
	if err != nil || status != "EXECUTING" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	legs, err := s.ListRebalanceLegs(ctx, "space", "delta-1")
	if err != nil || len(legs) != 1 || legs[0].Side != "BUY" {
		t.Fatalf("legs=%+v err=%v", legs, err)
	}
}

func TestExpiredOrderTerminalState(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ReconcileBalances(ctx, "space", "account", map[string]map[string]shared.Decimal{
		"USDT": {"available": shared.MustDecimal("20")},
	}); err != nil {
		t.Fatal(err)
	}
	engine := &command.Engine{Store: s, Adapter: &scriptedExchange{}}
	in := command.PlaceInput{
		SpaceID: "space", OrderID: "exp", ClientOrderID: "exp-cli",
		AccountID: "account", ChannelID: "channel", Symbol: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", Side: "BUY", Quantity: "1", Price: "10",
	}
	if _, err = engine.Place(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err = (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "space", "exp"); err != nil {
		t.Fatal(err)
	}
	got, err := engine.ReconcileExchangeTerminal(ctx, "space", "exp", "EXPIRED")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != string(order.Expired) {
		t.Fatalf("state=%s", got.State)
	}
}
