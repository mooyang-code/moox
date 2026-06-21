package view

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResultTablePrefixDoesNotCollideForSanitizedSpaceIDs(t *testing.T) {
	hyphenPrefix := resultTablePrefix("a-b")
	underscorePrefix := resultTablePrefix("a_b")

	require.NotEqual(t, hyphenPrefix, underscorePrefix)
	require.Contains(t, resultTableName("a-b", "view", 2, time.Unix(1, 0).UTC()), hyphenPrefix)
	require.Contains(t, resultTableName("a_b", "view", 2, time.Unix(1, 0).UTC()), underscorePrefix)
	require.Contains(t, resultTableName("a-b", "view", 2, time.Unix(1, 0).UTC()), "_v2_")
}
