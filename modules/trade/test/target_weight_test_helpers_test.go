package test

import (
	"context"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

// testTargetWeightResolver keeps the test fixtures focused on the event and
// convergence flows. Production Trade always receives a real resolver from
// the Strategy integration boundary.
type testTargetWeightResolver struct{}

func (testTargetWeightResolver) Resolve(
	_ context.Context,
	signalTime int64,
	request *tradeeventpb.LogicalAccountTargetWeightRequested,
	_ string,
) (targetapp.WeightConversion, error) {
	targets := make([]store.InstrumentTarget, 0, len(request.GetTargets()))
	for _, target := range request.GetTargets() {
		targets = append(targets, store.InstrumentTarget{
			InstrumentID: target.GetInstrumentId(),
			Quantity:     target.GetTargetWeight(),
		})
	}
	if signalTime <= 0 {
		signalTime = time.Now().UTC().UnixMilli()
	}
	return targetapp.WeightConversion{
		SignalTime:       signalTime,
		WeightsJSON:      "[]",
		Equity:           shared.MustDecimal("1"),
		EquitySourceTime: signalTime,
		ReferencePrices:  map[string]string{},
		QuantityTargets:  targets,
	}, nil
}
