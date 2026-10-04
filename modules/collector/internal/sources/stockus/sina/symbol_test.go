package sina

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderSymbolUsesSinaUSNamespace(t *testing.T) {
	got, err := ProviderSymbol("MSFT.XNAS")
	require.NoError(t, err)
	require.Equal(t, "gb_msft", got)
	_, err = ProviderSymbol("MSFT.XHKG")
	require.Error(t, err)
}
