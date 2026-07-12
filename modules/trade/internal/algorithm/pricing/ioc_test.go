package pricing

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIOCSlippage_Descriptor_ShouldReturnIOCSlippageV1(t *testing.T) {
	assert.Equal(t, execution.AlgorithmDescriptor{Name: "ioc_slippage", Version: "1"}, IOCSlippage{}.Descriptor())
}

func TestIOCSlippage_Quote_BuyDefaultSlippage_ShouldApplyBPS(t *testing.T) {
	ref := shared.MustDecimal("100")
	quote, err := IOCSlippage{}.Quote(context.Background(), execution.PricingInput{
		Side:           "BUY",
		ReferencePrice: ref,
		Parameters:     map[string]string{},
	})
	require.NoError(t, err)
	assert.Equal(t, "IOC", quote.TimeInForce)
	assert.Equal(t, 10, quote.SlippageBPS)
	assert.Equal(t, "100.1", quote.Price.String())
}

func TestIOCSlippage_Quote_SellCustomSlippage_ShouldApplyBPS(t *testing.T) {
	ref := shared.MustDecimal("100")
	quote, err := IOCSlippage{}.Quote(context.Background(), execution.PricingInput{
		Side:           "SELL",
		ReferencePrice: ref,
		Parameters:     map[string]string{"slippage_bps": "20"},
	})
	require.NoError(t, err)
	assert.Equal(t, 20, quote.SlippageBPS)
	assert.Equal(t, "99.8", quote.Price.String())
}
