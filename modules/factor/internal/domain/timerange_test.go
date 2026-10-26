package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeTimeRanges(t *testing.T) {
	require.Nil(t, MergeTimeRanges(nil, 60))
	merged := MergeTimeRanges([]TimeRange{{Start: 300, End: 360}, {Start: 0, End: 60}, {Start: 120, End: 180}, {Start: 60, End: 120}, {Start: 1000, End: 1000}}, 60)
	require.Equal(t, []TimeRange{{Start: 0, End: 180}, {Start: 300, End: 360}, {Start: 1000, End: 1000}}, merged)
}
