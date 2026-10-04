package sina

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderSymbolPreservesHongKongCodeWidth(t *testing.T) {
	got, err := ProviderSymbol("5.XHKG")
	require.NoError(t, err)
	require.Equal(t, "hk00005", got)
	got, err = ProviderSymbol("00005.XHKG")
	require.NoError(t, err)
	require.Equal(t, "hk00005", got)
	_, err = ProviderSymbol("00005.XSHG")
	require.Error(t, err)
}
