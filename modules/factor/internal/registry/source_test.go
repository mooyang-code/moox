package registry

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDependsFromSource(t *testing.T) {
	source := `
extra_data_dict = {
    "coin-cap": ["open_interest", "funding_rate"],
    "other": ["funding_rate"],
}`
	require.Equal(t, []string{"funding_rate", "open_interest"}, DependsFromSource(source))
	require.Empty(t, DependsFromSource("def signal(): pass"))
}
