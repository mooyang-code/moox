package view

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResultTableNameCarriesViewSpaceAndVersion(t *testing.T) {
	hyphenSpace := resultTableName("a-b", "kline_view", 2, time.Unix(1, 0).UTC())
	underscoreSpace := resultTableName("a_b", "kline_view", 2, time.Unix(1, 0).UTC())

	require.NotEqual(t, hyphenSpace, underscoreSpace)
	require.Equal(t, "view_", resultTablePrefix("a-b"))
	require.Contains(t, hyphenSpace, "view_kline_view_")
	require.Contains(t, underscoreSpace, "view_kline_view_")
	require.Contains(t, hyphenSpace, "_v2_")
}

func TestResultTableNameStartsWithViewID(t *testing.T) {
	name := resultTableName("crypto", "kline_view", 6, time.Unix(1, 0).UTC())

	require.Contains(t, name, "view_kline_view_")
	require.Contains(t, name, "_v6_")
}
