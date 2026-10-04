package index

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEastMoneySecIDUsesIndexExchangePrefix(t *testing.T) {
	got, err := EastMoneySecID("000001.XSHG")
	require.NoError(t, err)
	require.Equal(t, "1.000001", got)
	got, err = EastMoneySecID("399001.XSHE")
	require.NoError(t, err)
	require.Equal(t, "0.399001", got)
	_, err = EastMoneySecID("000001.XHKG")
	require.Error(t, err)
}
