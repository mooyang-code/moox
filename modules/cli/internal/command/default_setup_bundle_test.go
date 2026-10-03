package command

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func defaultSetupBundlePath(parts ...string) string {
	items := []string{"..", "..", "..", "..", "config", "setup"}
	return filepath.Join(append(items, parts...)...)
}

func TestDefaultSetupBundleDefinesBusinessAndInternalSpaces(t *testing.T) {
	seed, err := loadMetadataSeed(defaultSetupBundlePath("metadata.yaml"))
	require.NoError(t, err)

	var business, internal []string
	for _, space := range seed.Spaces {
		switch space.Attributes["scope"] {
		case "internal":
			internal = append(internal, space.SpaceID)
		default:
			business = append(business, space.SpaceID)
		}
	}
	slices.Sort(business)
	slices.Sort(internal)
	require.Equal(t, []string{"crypto", "stockcn", "stockhk", "stockus"}, business)
	require.Equal(t, []string{"mooxsys"}, internal)

	byID := make(map[string]seedSpace, len(seed.Spaces))
	for _, space := range seed.Spaces {
		byID[space.SpaceID] = space
	}
	require.Equal(t, "CN", byID["stockcn"].Market)
	require.Equal(t, "Asia/Shanghai", byID["stockcn"].Timezone)
	require.Equal(t, "crypto", byID["crypto"].Market)
	require.Equal(t, "UTC", byID["crypto"].Timezone)
}

func TestDefaultSetupBundleDefinesCompleteDatasets(t *testing.T) {
	seed, err := loadMetadataSeed(defaultSetupBundlePath("metadata.yaml"))
	require.NoError(t, err)
	require.NoError(t, validateReservedInternalSpaces(seed))
	require.NoError(t, validateSetupMetadataDependencies(seed))
	_, err = buildMetadataImportCalls(seed)
	require.NoError(t, err)

	datasetsBySpace := map[string][]string{}
	columnCount := map[string]int{}
	viewCount := map[string]int{}
	viewColumnCount := map[string]int{}
	for _, dataset := range seed.Datasets {
		datasetsBySpace[dataset.SpaceID] = append(datasetsBySpace[dataset.SpaceID], dataset.DatasetID)
		require.LessOrEqual(t, utf8.RuneCountInString(dataset.Name), 10, dataset.SpaceID+"/"+dataset.DatasetID)
		if dataset.SpaceID == "stockcn" && dataset.DatasetID == "dataset_stockcn_equity_kline" {
			require.Equal(t, []string{"1m"}, dataset.Freqs)
			require.Equal(t, "stockcn", dataset.DataSourceID)
		}
		if dataset.SpaceID == "crypto" {
			switch dataset.DatasetID {
			case "dataset_binance_kline_1m":
				require.Equal(t, []string{"1m"}, dataset.Freqs, dataset.DatasetID)
			default:
				require.Equal(t, []string{"1H"}, dataset.Freqs, dataset.DatasetID)
			}
		}
		if dataset.SpaceID == "crypto" {
			switch dataset.DatasetID {
			case "dataset_spot_kline_1h":
				require.Equal(t, "spot", dataset.Attributes["market_type"], dataset.DatasetID)
			case "dataset_perpetual_kline_1h":
				require.Equal(t, "swap", dataset.Attributes["market_type"], dataset.DatasetID)
			case "dataset_binance_kline_1m":
				require.Equal(t, "raw_collection", dataset.Attributes["dataset_role"], dataset.DatasetID)
				require.Equal(t, "storage-node-0", dataset.DataNodeID, dataset.DatasetID)
				require.Equal(t, "720h", dataset.KeepDuration, dataset.DatasetID)
			}
		}
	}
	for _, column := range seed.DatasetColumns {
		columnCount[column.SpaceID+"/"+column.DatasetID]++
	}
	for _, view := range seed.Views {
		require.LessOrEqual(t, utf8.RuneCountInString(view.Name), 10, view.SpaceID+"/"+view.ViewID)
		viewCount[view.SpaceID+"/"+view.PrimaryDatasetID]++
		if view.SpaceID == "crypto" {
			switch view.ViewID {
			case "view_binance_kline_1m":
				require.Contains(t, view.FilterJSON, `"freq":"1m"`, view.ViewID)
			default:
				require.Contains(t, view.FilterJSON, `"freq":"1H"`, view.ViewID)
			}
		}
	}
	for _, column := range seed.ViewColumns {
		viewColumnCount[column.SpaceID+"/"+column.ViewID]++
	}
	for _, dataset := range seed.Datasets {
		key := dataset.SpaceID + "/" + dataset.DatasetID
		require.Positive(t, columnCount[key], "Dataset %s has no columns", key)
		if dataset.DataKind == "time_series" {
			require.Positive(t, viewCount[key], "Dataset %s has no View", key)
		}
	}
	for _, view := range seed.Views {
		require.Positive(t, viewColumnCount[view.SpaceID+"/"+view.ViewID], "View %s/%s has no columns", view.SpaceID, view.ViewID)
	}

	for spaceID := range datasetsBySpace {
		slices.Sort(datasetsBySpace[spaceID])
	}
	require.Equal(t, []string{
		"dataset_stockcn_bond_kline",
		"dataset_stockcn_equity_kline",
		"dataset_stockcn_financial_statement_metric",
		"dataset_stockcn_financial_summary",
		"dataset_stockcn_index_kline",
	}, datasetsBySpace["stockcn"])
	require.Equal(t, []string{"dataset_binance_kline_1m", "dataset_perpetual_kline_1h", "dataset_spot_kline_1h"}, datasetsBySpace["crypto"])
}

func TestDefaultBinanceSharedTasksMatchDatasetAndViewColumns(t *testing.T) {
	const datasetID = "dataset_binance_kline_1m"
	const viewID = "view_binance_kline_1m"

	seed, err := loadMetadataSeed(defaultSetupBundlePath("metadata.yaml"))
	require.NoError(t, err)
	datasetColumns := map[string]bool{}
	for _, column := range seed.DatasetColumns {
		if column.SpaceID == "crypto" && column.DatasetID == datasetID {
			datasetColumns[column.ColumnName] = true
		}
	}
	viewColumns := map[string]bool{}
	for _, column := range seed.ViewColumns {
		if column.SpaceID == "crypto" && column.ViewID == viewID {
			viewColumns[column.ColumnName] = true
		}
	}

	content, err := os.ReadFile(defaultSetupBundlePath("collection-tasks.yaml"))
	require.NoError(t, err)
	var bundle struct {
		Tasks []struct {
			SpaceID       string `yaml:"space_id"`
			TaskName      string `yaml:"task_name"`
			ResultDataset string `yaml:"result_dataset_id"`
			ResultView    string `yaml:"result_view_id"`
			CollectParams struct {
				MarketType   string   `yaml:"market_type"`
				Frequency    string   `yaml:"frequency"`
				OutputFields []string `yaml:"output_fields"`
			} `yaml:"collect_params"`
		} `yaml:"tasks"`
	}
	require.NoError(t, yaml.Unmarshal(content, &bundle))

	marketTypes := map[string]bool{}
	for _, task := range bundle.Tasks {
		if task.SpaceID != "crypto" || task.ResultDataset != datasetID || task.ResultView != viewID || task.CollectParams.Frequency != "1m" {
			continue
		}
		marketTypes[task.CollectParams.MarketType] = true
		require.NotEmpty(t, task.CollectParams.OutputFields, task.TaskName)
		for _, field := range task.CollectParams.OutputFields {
			require.True(t, datasetColumns[field], "%s output %q is not a Dataset column", task.TaskName, field)
			require.True(t, viewColumns[datasetID+"."+field], "%s output %q is not a View column", task.TaskName, field)
		}
	}
	require.Equal(t, map[string]bool{"spot": true, "swap": true}, marketTypes)
}

func TestDefaultSetupBundleUsesOnlyFixedFiles(t *testing.T) {
	for _, name := range []string{
		"metadata.yaml",
		"collection-tasks.yaml",
		"dataset-health-policy.yaml",
		"service-deployments.yaml",
	} {
		_, err := os.Stat(defaultSetupBundlePath(name))
		require.NoError(t, err, name)
	}
	for _, path := range []string{
		"metadata-quant-initial.seed.yaml",
		"metadata-monitor-host.seed.yaml",
		"metadata-monitor-metrics.seed.yaml",
		"platform-local.seed.yaml",
	} {
		_, err := os.Stat(filepath.Join("..", "..", "..", "..", "examples", path))
		require.ErrorIs(t, err, os.ErrNotExist, path)
	}
}
