package command

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
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
		if dataset.SpaceID == "stockcn" && dataset.DatasetID == "dataset_stockcn_equity_kline_1m" {
			require.Equal(t, "1m", dataset.Freq)
			require.Equal(t, "stockcn", dataset.DataSourceID)
		}
	}
	for _, column := range seed.DatasetColumns {
		columnCount[column.SpaceID+"/"+column.DatasetID]++
	}
	for _, view := range seed.Views {
		require.LessOrEqual(t, utf8.RuneCountInString(view.Name), 10, view.SpaceID+"/"+view.ViewID)
		viewCount[view.SpaceID+"/"+view.PrimaryDatasetID]++
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
		"dataset_stockcn_bond_kline_1m",
		"dataset_stockcn_equity_kline_1m",
		"dataset_stockcn_financial_statement_metric",
		"dataset_stockcn_financial_summary",
		"dataset_stockcn_index_kline_1d",
	}, datasetsBySpace["stockcn"])
	require.Empty(t, datasetsBySpace["crypto"], "crypto results are task-owned Datasets that Collector creates")
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
