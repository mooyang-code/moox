package rebalance

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

type fakeSnapshotResolver struct{ priceErr error }

func (f fakeSnapshotResolver) ResolveLatestPrice(context.Context, string, string, *tradeeventpb.RebalanceTarget) (Market, error) {
	return Market{BaseAsset: "BTC", QuoteAsset: "USDT", Price: "100"}, f.priceErr
}
func (fakeSnapshotResolver) ResolveCurrentQuantity(context.Context, string, string, string) (shared.Decimal, error) {
	return shared.Zero(), nil
}
func (fakeSnapshotResolver) RoundQuantity(_ context.Context, _ string, value shared.Decimal) (shared.Decimal, error) {
	return value, nil
}

func validRequest() *tradeeventpb.RebalanceRequested {
	return &tradeeventpb.RebalanceRequested{
		RequestId: "event-1", StrategyRunId: "run-1", ExecutionBindingId: "exec-1",
		AccountId: "account-1", ChannelId: "channel-1", Mode: "paper",
		DataRevision: "rev-1", CapitalAmount: "1000", QuoteAsset: "USDT",
		Targets: []*tradeeventpb.RebalanceTarget{{
			InstrumentId: "BTC-USDT", Symbol: "BTC-USDT", MarketType: "spot", TargetWeight: "0.5",
		}},
	}
}

func TestRequestPlannerBuildsFixedCapitalTarget(t *testing.T) {
	input, err := (RequestPlanner{Resolver: fakeSnapshotResolver{}}).Build(context.Background(), "crypto", validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Targets) != 1 || input.Targets[0].Quantity.String() != "5" {
		t.Fatalf("targets = %+v", input.Targets)
	}
}

func TestRequestPlannerClassifiesContractAndSnapshotErrors(t *testing.T) {
	bad := validRequest()
	bad.Targets[0].TargetWeight = "2"
	_, err := (RequestPlanner{Resolver: fakeSnapshotResolver{}}).Build(context.Background(), "crypto", bad)
	if !IsPermanentRequestError(err) {
		t.Fatalf("error = %v, want permanent", err)
	}
	_, err = (RequestPlanner{Resolver: fakeSnapshotResolver{priceErr: errors.New("storage unavailable")}}).Build(context.Background(), "crypto", validRequest())
	if err == nil || IsPermanentRequestError(err) {
		t.Fatalf("error = %v, want transient", err)
	}
}
