package split

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSingle_Descriptor_ShouldReturnSingleV1(t *testing.T) {
	assert.Equal(t, execution.AlgorithmDescriptor{Name: "single", Version: "1"}, Single{}.Descriptor())
}

func TestSingle_Build_ValidInput_ShouldReturnOneSlice(t *testing.T) {
	qty := shared.MustDecimal("10")
	slices, err := Single{}.Build(context.Background(), execution.SplitInput{Quantity: qty})
	require.NoError(t, err)
	require.Len(t, slices, 1)
	assert.Equal(t, 1, slices[0].Sequence)
	assert.Equal(t, "10", slices[0].Quantity.String())
}

func TestFixedNotional_Descriptor_ShouldReturnFixedNotionalV1(t *testing.T) {
	assert.Equal(t, execution.AlgorithmDescriptor{Name: "fixed_notional", Version: "1"}, FixedNotional{}.Descriptor())
}

func TestFixedNotional_Build_ValidInput_ShouldPreserveQuantity(t *testing.T) {
	in := execution.SplitInput{
		Quantity:       shared.MustDecimal("10"),
		ReferencePrice: shared.MustDecimal("3"),
		MaxNotional:    shared.MustDecimal("9"),
	}
	slices, err := FixedNotional{}.Build(context.Background(), in)
	require.NoError(t, err)
	require.Len(t, slices, 4)

	sum := shared.Zero()
	for _, s := range slices {
		sum = sum.Add(s.Quantity)
	}
	assert.Equal(t, "10", sum.String())
}

func TestFixedNotional_Build_InvalidInput_ShouldReturnError(t *testing.T) {
	_, err := FixedNotional{}.Build(context.Background(), execution.SplitInput{
		Quantity:       shared.MustDecimal("10"),
		ReferencePrice: shared.Zero(),
		MaxNotional:    shared.MustDecimal("9"),
	})
	assert.Error(t, err)
}
