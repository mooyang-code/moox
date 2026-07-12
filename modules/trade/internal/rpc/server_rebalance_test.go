package rpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rebalanceStubAdapter struct{}

func (rebalanceStubAdapter) Place(_ context.Context, r exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-" + r.ClientOrderID, Status: "OPEN"}, nil
}
func (rebalanceStubAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}
func (rebalanceStubAdapter) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "OPEN"}, nil
}
func (rebalanceStubAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{BaseAsset: "BTC", QuoteAsset: "USDT"}, nil
}
func (rebalanceStubAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}
func (rebalanceStubAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}

func seedRebalanceKernel(t *testing.T) (*store.Store, *command.Engine, *Server) {
	t.Helper()
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	require.NoError(t, ks.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.PostLedger("crypto", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed-btc"), BizType: "seed", RefType: "test", RefID: "btc",
			Entries: []ledger.Entry{
				{AccountID: "clearing", Asset: "BTC", Bucket: "clearing", Amount: shared.MustDecimal("2").Neg()},
				{AccountID: "acct-1", Asset: "BTC", Bucket: "available", Amount: shared.MustDecimal("2")},
			},
		})
	}))
	engine := &command.Engine{Store: ks, Adapter: rebalanceStubAdapter{}}
	return ks, engine, New(nil, engine)
}

func TestServer_CreateRebalance_ValidPlan_ShouldSucceed(t *testing.T) {
	_, _, h := seedRebalanceKernel(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.CreateRebalance(ctx, &mooxpb.CreateRebalanceReq{
		RunId: "run-1", IdempotencyKey: "idem-1", AccountId: "acct-1", ChannelId: "chan-1",
		MarketSnapshotId: "m1", PositionSnapshotId: "p1", RulesVersion: "r1",
		Targets: []*mooxpb.TargetPosition{{Symbol: "BTCUSDT", Quantity: "0"}},
		Currents: []*mooxpb.CurrentPosition{{Symbol: "BTCUSDT", Quantity: "2"}},
		Markets: []*mooxpb.RebalanceMarket{{Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Price: "10"}},
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Equal(t, "PLANNED", rsp.Status)
}

func TestServer_AdvanceRebalance_PlannedRun_ShouldExecute(t *testing.T) {
	ks, engine, h := seedRebalanceKernel(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, err := h.CreateRebalance(ctx, &mooxpb.CreateRebalanceReq{
		RunId: "run-adv", IdempotencyKey: "idem-adv", AccountId: "acct-1", ChannelId: "chan-1",
		MarketSnapshotId: "m1", PositionSnapshotId: "p1", RulesVersion: "r1",
		Targets: []*mooxpb.TargetPosition{{Symbol: "BTCUSDT", Quantity: "0"}},
		Currents: []*mooxpb.CurrentPosition{{Symbol: "BTCUSDT", Quantity: "2"}},
		Markets: []*mooxpb.RebalanceMarket{{Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Price: "10"}},
	})
	require.NoError(t, err)
	rsp, err := h.AdvanceRebalance(ctx, &mooxpb.AdvanceRebalanceReq{
		RunId: "run-adv", AccountId: "acct-1", ChannelId: "chan-1",
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Equal(t, "EXECUTING", rsp.Status)
	legs, err := ks.ListRebalanceLegs(ctx, "crypto", "run-adv")
	require.NoError(t, err)
	require.Len(t, legs, 1)
	_, err = (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "crypto", legs[0].PlanID)
	require.NoError(t, err)
}
