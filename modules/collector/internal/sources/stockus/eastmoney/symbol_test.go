package eastmoney

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecIDUsesExchangeQualifiedUSSymbol(t *testing.T) {
	got, err := SecID("aapl.XNAS")
	require.NoError(t, err)
	require.Equal(t, "105.AAPL", got)
	_, err = SecID("AAPL.XHKG")
	require.Error(t, err)
}
