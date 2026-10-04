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

func TestNormalizeStableAndDivideStablePreserveSum(t *testing.T) {
	values, err := NormalizeStable([]Decimal{Must("1"), Must("1"), Must("1")})
	require.NoError(t, err)
	require.Equal(t, "1", values[0].Add(values[1]).Add(values[2]).String())
	allocated := DivideStable(Must("1"), []string{"a", "b", "c"})
	require.Equal(t, "1", allocated["a"].Add(allocated["b"]).Add(allocated["c"]).String())
	require.Equal(t, "0.333333333333333334", allocated["a"].String())
}
