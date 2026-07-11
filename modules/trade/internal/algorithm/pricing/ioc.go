package pricing

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type IOCSlippage struct{}

func (IOCSlippage) Descriptor() execution.AlgorithmDescriptor {
	return execution.AlgorithmDescriptor{Name: "ioc_slippage", Version: "1"}
}
func (IOCSlippage) Quote(_ context.Context, in execution.PricingInput) (execution.OrderQuote, error) {
	bps := 10
	if v := in.Parameters["slippage_bps"]; v == "20" {
		bps = 20
	}
	factor := shared.MustDecimal("1").Add(shared.MustDecimal(map[int]string{10: "0.001", 20: "0.002"}[bps]))
	if in.Side == "SELL" {
		factor = shared.MustDecimal("1").Sub(factor.Sub(shared.MustDecimal("1")))
	}
	return execution.OrderQuote{Price: in.ReferencePrice.Mul(factor), TimeInForce: "IOC", SlippageBPS: bps}, nil
}
