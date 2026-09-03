package bond

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertibleBondSymbolsKeepExchangeIdentity(t *testing.T) {
	got, err := EastMoneySecID("110001.XSHG")
	require.NoError(t, err)
	require.Equal(t, "1.110001", got)
	got, err = SinaSymbol("123001.XSHE")
	require.NoError(t, err)
	require.Equal(t, "sz123001", got)
	_, err = EastMoneySecID("600000.XSHG")
	require.NoError(t, err)
	_, err = SinaSymbol("110001.XBSE")
	require.Error(t, err)
}
