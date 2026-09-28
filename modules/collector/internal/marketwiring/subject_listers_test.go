package marketwiring

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewSubjectListersIncludesBinanceProducts(t *testing.T) {
	listers, err := NewSubjectListers()
	require.NoError(t, err)
	require.Equal(t, []string{"spot", "swap"}, listers.Supported()["binance"])
}
func TestNewSubjectListersCoversBuiltInTagSources(t *testing.T) {
	listers, err := NewSubjectListers()
	require.NoError(t, err)
	supported := listers.Supported()
	require.ElementsMatch(t, []string{"spot", "swap"}, supported["binance"])
	require.Contains(t, supported["eastmoney"], "equity", "the built-in cn_a_share tag must publish a subject_listing capability")
}
