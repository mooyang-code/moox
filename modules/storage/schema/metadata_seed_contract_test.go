package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

type metadataSeedGrainContract struct {
	Datasets []struct {
		SpaceID    string            `yaml:"space_id"`
		DatasetID  string            `yaml:"dataset_id"`
		DataKind   string            `yaml:"data_kind"`
		Freq       string            `yaml:"freq"`
		Attributes map[string]string `yaml:"attributes"`
	} `yaml:"datasets"`
	DatasetColumns []struct {
		SpaceID    string `yaml:"space_id"`
		DatasetID  string `yaml:"dataset_id"`
		ColumnName string `yaml:"column_name"`
	} `yaml:"dataset_columns"`
	Views []struct {
		SpaceID          string   `yaml:"space_id"`
		ViewID           string   `yaml:"view_id"`
		PrimaryDatasetID string   `yaml:"dataset_id"`
		GrainKeys        []string `yaml:"grain_keys"`
	} `yaml:"views"`
	ViewColumns []struct {
		SpaceID    string `yaml:"space_id"`
		ViewID     string `yaml:"view_id"`
		ColumnName string `yaml:"column_name"`
	} `yaml:"view_columns"`
	Devices []struct {
		DeviceID string `yaml:"device_id"`
		Engine   string `yaml:"engine"`
	} `yaml:"devices"`
}

func TestActiveMetadataSeedsUseCanonicalTimeSeriesViewGrain(t *testing.T) {
	wantGrain := []string{"subject_id", "freq", "data_time", "series_tag"}
	root := filepath.Join("..", "..", "..")
	seedPaths := []string{filepath.Join(root, "config", "setup", "metadata.yaml")}

	for _, seedPath := range seedPaths {
		t.Run(filepath.Base(filepath.Dir(seedPath))+"/"+filepath.Base(seedPath), func(t *testing.T) {
			raw, err := os.ReadFile(seedPath)
			require.NoError(t, err)
			var seed metadataSeedGrainContract
			require.NoError(t, yaml.Unmarshal(raw, &seed))

			kinds := make(map[string]string, len(seed.Datasets))
			for _, dataset := range seed.Datasets {
				kinds[dataset.SpaceID+"/"+dataset.DatasetID] = dataset.DataKind
			}
			for _, view := range seed.Views {
				datasetKey := view.SpaceID + "/" + view.PrimaryDatasetID
				kind, ok := kinds[datasetKey]
				require.True(t, ok, "%s/%s references missing primary Dataset %s", view.SpaceID, view.ViewID, datasetKey)
				if kind != "time_series" {
					continue
				}
				require.Equal(t, wantGrain, view.GrainKeys, "%s/%s", view.SpaceID, view.ViewID)
			}
		})
	}
}

func TestDefaultSetupSeedTimeSeriesDatasetsDeclareCanonicalFreq(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "setup", "metadata.yaml"))
	require.NoError(t, err)
	var seed metadataSeedGrainContract
	require.NoError(t, yaml.Unmarshal(raw, &seed))
	for _, dataset := range seed.Datasets {
		if dataset.DataKind == "time_series" {
			require.True(t, frequencypkg.IsCanonical(dataset.Freq), "%s/%s freq %q", dataset.SpaceID, dataset.DatasetID, dataset.Freq)
		}
	}
}

func TestDefaultSetupSeedDeclaresArchiveDevice(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "setup", "metadata.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var seed metadataSeedGrainContract
	require.NoError(t, yaml.Unmarshal(raw, &seed))
	for _, device := range seed.Devices {
		if device.DeviceID == "parquet-local" {
			require.Equal(t, "parquet", device.Engine)
			return
		}
	}
	t.Fatal("default setup metadata does not declare parquet-local")
}

func TestDefaultSetupSeedDeclaresCollectorPeriodDatasetOwners(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "setup", "metadata.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var seed metadataSeedGrainContract
	require.NoError(t, yaml.Unmarshal(raw, &seed))
	indexes := make(map[string]int, len(seed.Datasets))
	for i, dataset := range seed.Datasets {
		key := dataset.SpaceID + "/" + dataset.DatasetID
		require.NotContains(t, indexes, key, "duplicate Dataset identity")
		indexes[key] = i
	}
	owners := []string{
		"stockcn/dataset_stockcn_equity_kline_1m",
		"stockcn/dataset_stockcn_index_kline_1d",
		"stockcn/dataset_stockcn_bond_kline_1m",
		"stockhk/dataset_stockhk_equity_kline_1d",
		"stockus/dataset_stockus_equity_kline_1d",
	}
	for _, key := range owners {
		t.Run(key, func(t *testing.T) {
			index, found := indexes[key]
			require.True(t, found, "raw market Dataset is missing")
			dataset := seed.Datasets[index]
			require.Equal(t, "time_series", dataset.DataKind)
			require.True(t, strings.HasSuffix(dataset.DatasetID, "_"+dataset.Freq), "market Dataset ID must end with its freq %q", dataset.Freq)
			require.Equal(t, "collector", dataset.Attributes["owner_module"])
			require.Equal(t, "raw_collection", dataset.Attributes["dataset_role"])
		})
	}
	preserved := map[string]struct{ kind, role string }{
		"stockcn/dataset_stockcn_financial_statement_metric": {"record", ""},
		"stockcn/dataset_stockcn_financial_summary":          {"record", ""},
		"mooxsys/dataset_mooxsys_host_resource":              {"time_series", ""},
		"mooxsys/dataset_mooxsys_host_filesystem":            {"time_series", ""},
		"mooxsys/dataset_mooxsys_host_disk":                  {"time_series", ""},
		"mooxsys/dataset_mooxsys_host_network":               {"time_series", ""},
		"mooxsys/dataset_mooxsys_service_metrics":            {"time_series", ""},
	}
	for key, want := range preserved {
		t.Run(key, func(t *testing.T) {
			index, found := indexes[key]
			require.True(t, found, "non-Collector Dataset is missing")
			dataset := seed.Datasets[index]
			require.Equal(t, want.kind, dataset.DataKind)
			require.Empty(t, dataset.Attributes["owner_module"])
			require.Equal(t, want.role, dataset.Attributes["dataset_role"])
		})
	}
}
