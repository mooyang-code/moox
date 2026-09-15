package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/stretchr/testify/require"
)

func TestMergedDatasetDefinitionRejectsSourceChangeAfterEnablePersist(t *testing.T) {
	repo := openMergedDatasetRepo(t)
	def := validSpotSwapStoreDataset()
	require.NoError(t, repo.Save(context.Background(), def))
	require.NoError(t, repo.Enable(context.Background(), def.DatasetID))
	def.Sources = def.Sources[:1]
	require.ErrorContains(t, repo.Save(context.Background(), def), "input semantics")
}

func TestMergedDatasetDefinitionReconcileStorageIsIdempotentAfterLostResponse(t *testing.T) {
	repo := openMergedDatasetRepo(t)
	def := validSpotSwapStoreDataset()
	require.NoError(t, repo.Save(context.Background(), def))
	client := &recordingDatasetClient{schemaID: "schema-ohlcv"}
	require.NoError(t, repo.ReconcileStorage(context.Background(), def.DatasetID, client))
	require.Equal(t, 1, client.creates)
	got, err := repo.Get(context.Background(), def.DatasetID)
	require.NoError(t, err)
	require.Equal(t, domain.StorageResourceCreated, got.StorageResourceState)
	require.Equal(t, "schema-ohlcv", got.StorageSchemaID)

	client.failAfterCreate = true
	client.alreadyExists = true
	require.NoError(t, repo.ReconcileStorage(context.Background(), def.DatasetID, client))
	require.Equal(t, 1, client.creates, "created resources must not be created again")

	lost := &recordingDatasetClient{schemaID: "schema-ohlcv", alreadyExists: true}
	fresh := openMergedDatasetRepo(t)
	require.NoError(t, fresh.Save(context.Background(), validSpotSwapStoreDataset()))
	require.NoError(t, fresh.ReconcileStorage(context.Background(), def.DatasetID, lost))
	require.Equal(t, 1, lost.creates)
	again, err := fresh.Get(context.Background(), def.DatasetID)
	require.NoError(t, err)
	require.Equal(t, domain.StorageResourceCreated, again.StorageResourceState)
	require.Equal(t, "schema-ohlcv", again.StorageSchemaID)
}

func TestMergedDatasetEnableKeepsConfiguredSnapshotID(t *testing.T) {
	repo := openMergedDatasetRepo(t)
	def := validSpotSwapStoreDataset()
	require.NoError(t, repo.Save(context.Background(), def))
	require.NoError(t, repo.EnableSnapshot(context.Background(), def.DatasetID, "snap-1"))
	got, err := repo.Get(context.Background(), def.DatasetID)
	require.NoError(t, err)
	require.True(t, got.Enabled)
	require.Equal(t, "snap-1", got.ConfigSnapshotID)
	require.NoError(t, repo.EnableSnapshot(context.Background(), def.DatasetID, "snap-1"))
	again, err := repo.Get(context.Background(), def.DatasetID)
	require.NoError(t, err)
	require.Equal(t, "snap-1", again.ConfigSnapshotID)
}

func TestMergedDatasetDefinitionPersistsImmutableSnapshot(t *testing.T) {
	repo := openMergedDatasetRepo(t)
	def := validSpotSwapStoreDataset()
	require.NoError(t, repo.Save(context.Background(), def))
	require.NoError(t, repo.Enable(context.Background(), def.DatasetID))
	got, err := repo.Get(context.Background(), def.DatasetID)
	require.NoError(t, err)
	require.NotEmpty(t, got.ConfigSnapshotID)
	require.True(t, got.Enabled)
	snapshots, err := repo.ListSnapshots(context.Background(), def.DatasetID)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	require.Equal(t, got.ConfigSnapshotID, snapshots[0].SnapshotID)
}

type recordingDatasetClient struct {
	schemaID        string
	creates         int
	failAfterCreate bool
	alreadyExists   bool
}

func (c *recordingDatasetClient) EnsureDataset(_ context.Context, def domain.MergedDataset) (string, error) {
	c.creates++
	if c.failAfterCreate {
		return "", errors.New("lost create response")
	}
	if c.alreadyExists {
		return c.schemaID, nil
	}
	return c.schemaID, nil
}

func openMergedDatasetRepo(t *testing.T) *MergedDatasetRepository {
	t.Helper()
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	return db.MergedDatasets()
}

func validSpotSwapStoreDataset() domain.MergedDataset {
	spot := "dataset_binance_spot_kline_1m"
	swap := "dataset_binance_swap_kline_1m"
	fields := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}
	def := domain.MergedDataset{
		DatasetID: "mdataset_binance_kline_1m",
		SpaceID:   "crypto",
		Frequency: "1m",
		KeyContract: domain.KeyContract{
			SubjectID: "subject_id", Frequency: "frequency", PeriodTime: "period_time", SeriesTag: "series_tag",
			PeriodBoundary: "close",
		},
		ObjectSet: []string{"BTC-USDT", "ETH-USDT"},
		Sources: []domain.SourceDatasetRef{
			{DatasetID: spot, Frequency: "1m", PeriodBoundary: "close", Fields: append([]string(nil), fields...)},
			{DatasetID: swap, Frequency: "1m", PeriodBoundary: "close", Fields: append([]string(nil), fields...)},
		},
		MergeMode: domain.MergeModeSystem,
	}
	for _, source := range def.Sources {
		for _, field := range source.Fields {
			def.FieldMappings = append(def.FieldMappings, domain.FieldMapping{
				SourceDatasetID: source.DatasetID, SourceField: field, TargetField: domain.MappedSourceField(source.DatasetID, field),
			})
		}
	}
	return def
}
