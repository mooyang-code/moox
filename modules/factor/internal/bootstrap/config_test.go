package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFactorConfigContainsOnlyRuntimeInputs(t *testing.T) {
	cfg := Default()
	require.Equal(t, 2048, cfg.Scheduler.QueueCapacity)
	require.Equal(t, 1, cfg.Scheduler.MaxRetry)
	require.NotEmpty(t, cfg.Engine.PythonBin)
	require.NotEmpty(t, cfg.Engine.FactorsDir)
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("engine:\n  sections_dir: ./sections\n"), 0o644))
	_, err := Load(path)
	require.Error(t, err)
}
