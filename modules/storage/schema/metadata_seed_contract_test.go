package schema

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

type metadataSeedGrainContract struct {
	Datasets []struct {
		SpaceID    string            `yaml:"space_id"`
		DatasetID  string            `yaml:"dataset_id"`
		DataKind   string            `yaml:"data_kind"`
		Freqs      []string          `yaml:"freqs"`
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

func TestBinanceOneMinuteTaskOutputFieldsExistInSeededDatasetAndView(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	metadataPath := filepath.Join(root, "config", "setup", "metadata.yaml")
	metadataRaw, err := os.ReadFile(metadataPath)
	require.NoError(t, err)
	var metadata metadataSeedGrainContract
	require.NoError(t, yaml.Unmarshal(metadataRaw, &metadata))

	var datasetFields, viewFields = map[string]struct{}{}, map[string]struct{}{}
	for _, column := range metadata.DatasetColumns {
		if column.SpaceID == "crypto" && column.DatasetID == "dataset_binance_kline_1m" {
			datasetFields[column.ColumnName] = struct{}{}
		}
	}
	for _, column := range metadata.ViewColumns {
		if column.SpaceID == "crypto" && column.ViewID == "view_binance_kline_1m" {
			viewFields[column.ColumnName] = struct{}{}
		}
	}

	tasksPath := filepath.Join(root, "config", "setup", "collection-tasks.yaml")
	tasksRaw, err := os.ReadFile(tasksPath)
	require.NoError(t, err)
	var tasks struct {
		Tasks []struct {
			SpaceID       string `yaml:"space_id"`
			DataType      string `yaml:"data_type"`
			ResultDataset string `yaml:"result_dataset_id"`
			ResultView    string `yaml:"result_view_id"`
			CollectParams struct {
				Provider     string   `yaml:"provider"`
				MarketType   string   `yaml:"market_type"`
				Frequency    string   `yaml:"frequency"`
				OutputFields []string `yaml:"output_fields"`
			} `yaml:"collect_params"`
		} `yaml:"tasks"`
	}
	require.NoError(t, yaml.Unmarshal(tasksRaw, &tasks))

	seenMarkets := make(map[string]struct{})
	for _, task := range tasks.Tasks {
		params := task.CollectParams
		if task.SpaceID != "crypto" || task.DataType != "kline" || params.Provider != "binance" || params.Frequency != "1m" {
			continue
		}
		if params.MarketType != "spot" && params.MarketType != "swap" {
			continue
		}
		seenMarkets[params.MarketType] = struct{}{}
		require.Equal(t, "dataset_binance_kline_1m", task.ResultDataset, "%s task Dataset", params.MarketType)
		require.Equal(t, "view_binance_kline_1m", task.ResultView, "%s task View", params.MarketType)
		require.NotEmpty(t, params.OutputFields, "%s task output_fields", params.MarketType)
		for _, field := range params.OutputFields {
			require.Contains(t, datasetFields, field, "%s output field missing from Dataset", field)
			require.Contains(t, viewFields, task.ResultDataset+"."+field, "%s output field missing from View", field)
		}
	}
	require.Equal(t, map[string]struct{}{"spot": {}, "swap": {}}, seenMarkets, "1m tasks must share the canonical Dataset and View")
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
	owners := map[string][]string{
		"stockcn/dataset_stockcn_equity_kline": {"1m"},
		"stockcn/dataset_stockcn_index_kline":  {"1H", "1d"},
		"stockcn/dataset_stockcn_bond_kline":   {"1m", "1d"},
		"stockhk/dataset_stockhk_equity_kline": {"1m", "1d", "1w", "1M"},
		"stockus/dataset_stockus_equity_kline": {"1m", "1d", "1w", "1M"},
		"crypto/dataset_binance_kline_1m":      {"1m"},
		"crypto/dataset_spot_kline_1h":         {"1H"},
		"crypto/dataset_perpetual_kline_1h":    {"1H"},
	}
	for key, freqs := range owners {
		t.Run(key, func(t *testing.T) {
			index, found := indexes[key]
			require.True(t, found, "raw market Dataset is missing")
			dataset := seed.Datasets[index]
			require.Equal(t, "time_series", dataset.DataKind)
			require.Equal(t, freqs, dataset.Freqs)
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
