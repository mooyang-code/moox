package metadata

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"

	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	"github.com/stretchr/testify/require"
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
	got := make([]string, 0, len(seed.Views))
	for _, view := range seed.Views {
		got = append(got, view.SpaceID+"/"+view.ViewID)
	}
	sort.Strings(got)
	want := []string{
		"crypto/view_crypto_spot_kline_1m",
		"crypto/view_crypto_swap_kline_1m",
		"crypto/view_crypto_swap_kline_1h",
		"crypto/view_crypto_spot_kline_1h",
		"mooxsys/view_mooxsys_host_disk",
		"mooxsys/view_mooxsys_host_fs",
		"mooxsys/view_mooxsys_host_net",
		"mooxsys/view_mooxsys_host_resource",
		"mooxsys/view_mooxsys_service_metrics",
		"stockcn/view_stockcn_bond_kline_1m",
		"stockcn/view_stockcn_index_kline_1d",
		"stockcn/view_stockcn_equity_kline_1m",
		"stockhk/view_stockhk_equity_kline_1d",
		"stockus/view_stockus_equity_kline_1d",
	}
	sort.Strings(want)
	require.Equal(t, want, got)
}

func TestImportEntitiesRequiresDeploymentRegisteredDataNode(t *testing.T) {
	ctx := context.Background()
	store := openSeedTestStore(t)
	seed := seedFile{
		Spaces:      []seedSpace{{SpaceID: "space", Name: "Space", Status: "active"}},
		DataSources: []seedDataSource{{SpaceID: "space", DataSourceID: "source", Name: "Source", Kind: "internal", Status: "active"}},
		Datasets: []seedDataset{{
			SpaceID: "space", DatasetID: "dataset", DataSourceID: "source", Name: "Dataset",
			DataKind: "time_series", DataNodeID: "storage-node-0", KeepDuration: "1h", Status: "active",
		}},
	}

	_, err := importEntities(ctx, store, seed)
	require.Error(t, err)
	// The preflight fails before any logical metadata is written. SQLite
	// exposes the missing DataNode as sql.ErrNoRows through GetDataNode.
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = store.GetSpace(ctx, "space")
	require.ErrorIs(t, err, sql.ErrNoRows)

	_, err = store.RegisterDataNode(ctx, "storage-node-0", "ip://127.0.0.1:20107", "local node")
	require.NoError(t, err)
	result, err := importEntities(ctx, store, seed)
	require.NoError(t, err)
	require.Equal(t, 1, result.Datasets)
	dataset, err := store.GetDataset(ctx, "space", "dataset")
	require.NoError(t, err)
	require.Equal(t, "storage-node-0", dataset.GetDataNodeId())
	require.Equal(t, "1h0m0s", dataset.GetKeepDuration())
	require.Equal(t, "disabled", dataset.GetStatus(), "seed status must not activate a Dataset")
}

func TestImportEntitiesValidatesDatasetBindingBeforeWrites(t *testing.T) {
	ctx := context.Background()
	store := openSeedTestStore(t)
	_, err := store.RegisterDataNode(ctx, "storage-node-0", "ip://127.0.0.1:20107", "local node")
	require.NoError(t, err)
	seed := seedFile{
		Spaces:   []seedSpace{{SpaceID: "space", Name: "Space"}},
		Datasets: []seedDataset{{SpaceID: "space", DatasetID: "dataset", DataNodeID: "storage-node-0"}},
	}

	_, err = importEntities(ctx, store, seed)
	require.ErrorContains(t, err, "keep_duration is required")
	_, err = store.GetSpace(ctx, "space")
	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestSeedViewColumnAttributesDefaultsInternalDisplayName(t *testing.T) {
	got := seedViewColumnAttributes(seedViewColumn{
		SpaceID: "mooxsys", ColumnName: "cpu_usage_percent",
	})
	require.Equal(t, "cpu_usage_percent", got["display_name"])

	external := seedViewColumnAttributes(seedViewColumn{
		SpaceID: "crypto", ColumnName: "close",
	})
	require.NotContains(t, external, "display_name")
}
