package rebalance

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

type fakeSnapshotResolver struct {
	priceErr      error
	roundedSymbol string
	channel       Channel
	currents      map[string]shared.Decimal
}

func (f fakeSnapshotResolver) ResolveChannel(context.Context, string, string, string, string) (Channel, error) {
	if f.channel.MarketType == "" {
		return Channel{MarketType: "spot"}, nil
	}
	return f.channel, nil
}
func (f fakeSnapshotResolver) ResolveLatestPrice(context.Context, string, string, string, *tradeeventpb.RebalanceTarget) (Market, error) {
	return Market{BaseAsset: "BTC", QuoteAsset: "USDT", Price: "100"}, f.priceErr
}
func (f fakeSnapshotResolver) ResolveCurrentQuantities(context.Context, string, string) (map[string]shared.Decimal, error) {
	return f.currents, nil
}
func (f *fakeSnapshotResolver) RoundQuantity(_ context.Context, _, _, _, symbol string, value shared.Decimal) (shared.Decimal, error) {
	f.roundedSymbol = symbol
	return value, nil
}

func validRequest() *tradeeventpb.RebalanceRequested {
	return &tradeeventpb.RebalanceRequested{
		RequestId: "event-1", StrategyRunId: "run-1", ExecutionBindingId: "exec-1",
		AccountId: "account-1", ChannelId: "channel-1", Mode: "paper",
		DataRevision: "rev-1", CapitalAmount: "1000", QuoteAsset: "USDT",
		Targets: []*tradeeventpb.RebalanceTarget{{
			InstrumentId: "instrument-btc-usdt", Symbol: "BTC-USDT", MarketType: "spot", TargetWeight: "0.5",
		}},
	}
}

func TestRequestPlannerBuildsFixedCapitalTarget(t *testing.T) {
	resolver := &fakeSnapshotResolver{}
	input, err := (RequestPlanner{Resolver: resolver}).Build(context.Background(), "crypto", validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Targets) != 1 || input.Targets[0].Quantity.String() != "5" {
		t.Fatalf("targets = %+v", input.Targets)
	}
	if resolver.roundedSymbol != "BTC-USDT" {
		t.Fatalf("rounded symbol = %q", resolver.roundedSymbol)
	}
}

func TestRequestPlannerFullTargetClosesOmittedPositionsIncludingEmptyTargets(t *testing.T) {
	request := validRequest()
	request.Targets = nil
	input, err := (RequestPlanner{Resolver: &fakeSnapshotResolver{
		currents: map[string]shared.Decimal{"BTC-USDT": shared.MustDecimal("2")},
	}}).Build(context.Background(), "crypto", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Targets) != 1 || input.Targets[0].Symbol != "BTC-USDT" || !input.Targets[0].Quantity.IsZero() {
		t.Fatalf("targets = %+v", input.Targets)
	}
	if len(input.Currents) != 1 || input.Currents[0].Quantity.String() != "2" {
		t.Fatalf("currents = %+v", input.Currents)
	}
}

func TestRequestPlannerAllowsShortOnlyForSwap(t *testing.T) {
	spot := validRequest()
	spot.Targets[0].TargetWeight = "-0.5"
	_, err := (RequestPlanner{Resolver: &fakeSnapshotResolver{}}).Build(context.Background(), "crypto", spot)
	if !IsPermanentRequestError(err) {
		t.Fatalf("spot short error = %v, want permanent", err)
	}

	swap := validRequest()
	swap.Targets[0].MarketType = "swap"
	swap.Targets[0].TargetWeight = "-0.5"
	input, err := (RequestPlanner{Resolver: &fakeSnapshotResolver{channel: Channel{MarketType: "swap"}}}).Build(context.Background(), "crypto", swap)
	if err != nil {
		t.Fatal(err)
	}
	if got := input.Targets[0].Quantity.String(); got != "-5" {
		t.Fatalf("swap short quantity = %s", got)
	}
}

func TestRequestPlannerRejectsQuoteMismatch(t *testing.T) {
	request := validRequest()
	request.QuoteAsset = "USDC"
	_, err := (RequestPlanner{Resolver: &fakeSnapshotResolver{}}).Build(context.Background(), "crypto", request)
	if !IsPermanentRequestError(err) {
		t.Fatalf("error = %v, want permanent", err)
	}
}

func TestRequestPlannerClassifiesContractAndSnapshotErrors(t *testing.T) {
	bad := validRequest()
	bad.Targets[0].TargetWeight = "2"
	_, err := (RequestPlanner{Resolver: &fakeSnapshotResolver{}}).Build(context.Background(), "crypto", bad)
	if !IsPermanentRequestError(err) {
		t.Fatalf("error = %v, want permanent", err)
	}
	_, err = (RequestPlanner{Resolver: &fakeSnapshotResolver{priceErr: errors.New("storage unavailable")}}).Build(context.Background(), "crypto", validRequest())
	if err == nil || IsPermanentRequestError(err) {
		t.Fatalf("error = %v, want transient", err)
	}
}
