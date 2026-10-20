package unitinstall

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
)

func TestDiffInventoryReportsAddedChangedAndRemovedPaths(t *testing.T) {
	sealed := []unitpackage.File{
		{Path: "bin/moox-trade", SHA256: "sha256:a", Mode: 0o755, Size: 1},
		{Path: "trade/config/app.yaml", SHA256: "sha256:b", Mode: 0o600, Size: 2},
		{Path: "trade/factors/old.py", SHA256: "sha256:c", Mode: 0o644, Size: 3},
		{Path: "trade/schema/trade.sql", SHA256: "sha256:d", Mode: 0o644, Size: 4},
	}
	actual := []unitpackage.File{
		{Path: "bin/moox-trade", SHA256: "sha256:z", Mode: 0o755, Size: 9},
		{Path: "trade/config/app.yaml", SHA256: "sha256:b", Mode: 0o644, Size: 2},
		{Path: "trade/config/new.yaml", SHA256: "sha256:e", Mode: 0o600, Size: 5},
		{Path: "trade/schema/trade.sql", SHA256: "sha256:d", Mode: 0o644, Size: 4},
	}
	var result ResealResult
	diffInventory(sealed, actual, &result)
	require.Equal(t, []string{"trade/config/new.yaml"}, result.Added)
	require.Equal(t, []string{"bin/moox-trade", "trade/config/app.yaml"}, result.Changed, "内容或权限变化都算变化")
	require.Equal(t, []string{"trade/factors/old.py"}, result.Removed)
	require.Equal(t, 1, result.Unchanged)

	var same ResealResult
	diffInventory(sealed, sealed, &same)
	require.Empty(t, same.Added)
	require.Empty(t, same.Changed)
	require.Empty(t, same.Removed)
	require.Equal(t, 4, same.Unchanged)
}
