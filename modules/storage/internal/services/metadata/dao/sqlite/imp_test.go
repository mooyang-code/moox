package sqlite

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitSQLiteImpWorksWithoutCGO(t *testing.T) {
	configDir := t.TempDir()
	dbPath := filepath.Join(configDir, "metadata.db")
	configContent := []byte("metadata_database:\n  storage_device: \"sqlite:" + dbPath + "\"\n")
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "metadata.yaml"), configContent, 0o644))

	t.Setenv("STORAGE_CONFIG_PATH", configDir)

	imp, err := InitSQLiteImp()
	require.NoError(t, err)
	require.NotNil(t, imp)
}
