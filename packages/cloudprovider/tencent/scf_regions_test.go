package tencent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSCFRegionsAreCompleteAndUnique(t *testing.T) {
	regions := SCFRegions()
	require.Len(t, regions, 18)
	seen := make(map[string]struct{}, len(regions))
	for _, region := range regions {
		require.NotEmpty(t, region.Code)
		require.NotEmpty(t, region.Name)
		require.NotEmpty(t, region.Tag)
		_, duplicate := seen[region.Code]
		require.False(t, duplicate, region.Code)
		seen[region.Code] = struct{}{}
	}
	require.True(t, IsSCFRegion("ap-beijing"))
	require.True(t, IsSCFRegion("ap-seoul"))
	require.True(t, IsSCFRegion("na-ashburn"))
	require.True(t, IsSCFRegion("ap-jakarta"))
	require.True(t, IsSCFRegion("sa-saopaulo"))
	require.False(t, IsSCFRegion("na-toronto"))
	require.False(t, IsSCFRegion("ap-unknown"))
}
