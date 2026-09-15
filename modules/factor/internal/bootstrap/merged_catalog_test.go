package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestSeedEngineMergedDatasetsActivatesConfiguredSnapshot(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "engine.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	cfg, err := LoadEngineApplicationConfig("../../config/engine-app.yaml")
	require.NoError(t, err)
	require.NoError(t, SeedMergedDatasets(context.Background(), db, cfg.Definitions))
	got, err := db.MergedDatasets().Get(context.Background(), "mdataset_binance_kline_1m")
	require.NoError(t, err)
	require.True(t, got.Enabled)
	require.Equal(t, "snap-1", got.ConfigSnapshotID)
	require.Equal(t, domain.MergeModeSystem, got.MergeMode)
	require.Len(t, got.Sources, 2)
	require.NoError(t, SeedMergedDatasets(context.Background(), db, cfg.Definitions))
	again, err := db.MergedDatasets().Get(context.Background(), "mdataset_binance_kline_1m")
	require.NoError(t, err)
	require.Equal(t, "snap-1", again.ConfigSnapshotID)
}
