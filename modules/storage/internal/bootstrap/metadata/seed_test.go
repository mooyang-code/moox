package metadata

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v2"
)

func openSeedTestStore(t *testing.T) *metasqlite.Store {
	t.Helper()
	ctx := context.Background()
	store, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "schema", "metadata.sql"),
	})
	require.NoError(t, err)
	require.NoError(t, store.InitSchema(ctx))
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestDefaultViewInventory(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "config", "setup", "metadata.yaml"))
	require.NoError(t, err)
	var seed seedFile
	// The full example also contains deployment-only fields not needed by the
	// seed importer. Decode permissively here because this test only asserts the
	// View inventory.
	require.NoError(t, yaml.Unmarshal(raw, &seed))
	// 默认种子不再声明 View：采集结果的 View 由采集任务派生，主机快照改存 Monitor。
	require.Empty(t, seed.Views)
}

func TestDefaultSeedImportsIntoEmptyStoreWithDatasetFrequencies(t *testing.T) {
	ctx := context.Background()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "config", "setup", "metadata.yaml"))
	require.NoError(t, err)
	var seed seedFile
	require.NoError(t, yaml.UnmarshalStrict(raw, &seed))
	store := openSeedTestStore(t)

	result, err := importEntities(ctx, store, seed)
	require.NoError(t, err)
	require.Equal(t, len(seed.Datasets), result.Datasets)
	require.Equal(t, len(seed.Views), result.Views)
	for _, item := range seed.Views {
		view, err := store.GetView(ctx, item.SpaceID, item.ViewID)
		require.NoError(t, err)
		dataset, err := store.GetDataset(ctx, item.SpaceID, item.PrimaryDatasetID)
		require.NoError(t, err)
		require.NotEmpty(t, view.GetFreq(), "%s/%s", item.SpaceID, item.ViewID)
		require.Equal(t, dataset.GetFreq(), view.GetFreq(), "%s/%s takes its Dataset freq", item.SpaceID, item.ViewID)
	}
}

// 种子新建的数据集直接启用；重新导入不改已有数据集的启停状态。
func TestImportEntitiesCreatesActiveDatasetsAndKeepsStatusOnReimport(t *testing.T) {
	ctx := context.Background()
	store := openSeedTestStore(t)
	seed := seedFile{
		Spaces:      []seedSpace{{SpaceID: "space", Name: "Space", Status: "active"}},
		DataSources: []seedDataSource{{SpaceID: "space", DataSourceID: "source", Name: "Source", Status: "active"}},
		Datasets: []seedDataset{{
			SpaceID: "space", DatasetID: "dataset", DataSourceID: "source", Name: "Dataset",
			DataKind: "time_series", Freq: "1m",
		}},
	}
	result, err := importEntities(ctx, store, seed)
	require.NoError(t, err)
	require.Equal(t, 1, result.Datasets)
	dataset, err := store.GetDataset(ctx, "space", "dataset")
	require.NoError(t, err)
	require.Equal(t, "active", dataset.GetStatus())

	disabled := proto.Clone(dataset).(*pb.Dataset)
	disabled.Status = "disabled"
	_, err = store.UpsertDataset(ctx, disabled)
	require.NoError(t, err)
	_, err = importEntities(ctx, store, seed)
	require.NoError(t, err)
	dataset, err = store.GetDataset(ctx, "space", "dataset")
	require.NoError(t, err)
	require.Equal(t, "disabled", dataset.GetStatus(), "重新导入不改启停状态")
}
