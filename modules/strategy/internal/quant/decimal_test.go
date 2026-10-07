package quant

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecimalCanonicalArithmetic(t *testing.T) {
	a := Must("1.25")
	b := Must("0.75")
	require.Equal(t, "2", a.Add(b).String())
	require.Equal(t, "0.5", a.Sub(b).String())
	require.Equal(t, "-1.25", a.Neg().String())
	for _, raw := range []string{"1e3", "+1", ".5", "1.", "01", "NaN", "Inf", "1.1234567890123456789"} {
		_, err := Parse(raw)
		require.Error(t, err, raw)
	}
}
