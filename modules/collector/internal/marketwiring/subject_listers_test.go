package marketwiring

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	"github.com/stretchr/testify/require"
)

func TestNewSubjectListersIncludesBinanceProducts(t *testing.T) {
	listers, _, err := NewSubjectListers()
	require.NoError(t, err)
	require.Equal(t, []string{"spot", "swap"}, listers.Supported()["binance"])
}

func TestNewSubjectListersCoversBuiltInTagSources(t *testing.T) {
	listers, indexes, err := NewSubjectListers()
	require.NoError(t, err)
	supported := listers.Supported()
	require.ElementsMatch(t, []string{"spot", "swap"}, supported["binance"])
	require.Contains(t, supported["eastmoney"], "equity", "the built-in cn_a_share tag must be listed by a subject lister")
	require.Contains(t, indexes, subjectsync.IndexListerKey{Source: "eastmoney", Index: "000300"}, "内置 cn_hs300 标签要有成分股 lister")
}
