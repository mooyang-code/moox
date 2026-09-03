package eastmoney

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecIDPreservesHongKongLeadingZeros(t *testing.T) {
	got, err := SecID("00005.XHKG")
	require.NoError(t, err)
	require.Equal(t, "116.00005", got)
	got, err = SecID("5.XHKG")
	require.NoError(t, err)
	require.Equal(t, "116.00005", got)
	_, err = SecID("00005.XSHG")
	require.Error(t, err)
	_, err = SecID("ABCDE.XHKG")
	require.Error(t, err)
}
