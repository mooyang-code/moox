package merge

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeAssemblerLoadsExampleSpotSwapDefinition(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	cfg, err := LoadProcessConfig(filepath.Join(filepath.Dir(file), "..", "..", "config", "merge-app.yaml"))
	require.NoError(t, err)
	require.Equal(t, "factor-merge-1", cfg.MergeID)
	require.Equal(t, "mdataset_binance_kline_1m", cfg.Definitions[0].DatasetID)
	require.Equal(t, "dataset_binance_spot_kline_1m", cfg.Definitions[0].Sources[0].DatasetID)
	require.Equal(t, "dataset_binance_swap_kline_1m", cfg.Definitions[0].Sources[1].DatasetID)
	require.Contains(t, cfg.Definitions[0].FieldMappings[0].TargetField, "__")
}
