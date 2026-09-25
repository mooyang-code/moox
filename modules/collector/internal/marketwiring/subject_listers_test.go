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
