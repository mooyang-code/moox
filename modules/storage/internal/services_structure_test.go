package internal_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStorageInternalServiceLayerDirectories(t *testing.T) {
	root := filepath.Join("..", "internal")

	serviceDirs, err := os.ReadDir(filepath.Join(root, "services"))
	require.NoError(t, err)

	allowedServices := map[string]bool{
		"access":  true,
		"archive": true,
		"primary": true,
		"search":  true,
		"view":    true,
	}
	for _, entry := range serviceDirs {
		if !entry.IsDir() || entry.Name() == ".claude" {
			continue
		}
		require.Truef(t, allowedServices[entry.Name()], "internal/services/%s should be moved to core or infra instead of staying under services", entry.Name())
	}

	expectedDirs := []string{
		"config",
		"core/eventbus",
		"core/metadata",
		"core/router",
		"core/schema",
		"infra/device",
		"infra/metadata/sqlite",
		"infra/transport",
		"services/access",
		"services/primary",
		"services/archive",
		"services/search",
		"services/view",
	}
	for _, dir := range expectedDirs {
		info, err := os.Stat(filepath.Join(root, dir))
		require.NoErrorf(t, err, "expected directory internal/%s", dir)
		require.Truef(t, info.IsDir(), "internal/%s should be a directory", dir)
	}
}
