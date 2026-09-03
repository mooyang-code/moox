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
		if dataset.SpaceID == "crypto" && dataset.DatasetID != "dataset_binance_spot_symbols" && dataset.DatasetID != "dataset_binance_swap_symbols" && dataset.DatasetID != "dataset_binance_spot_kline_1m" && dataset.DatasetID != "dataset_binance_swap_kline_1m" {
			require.Equal(t, []string{"1H"}, dataset.Freqs, dataset.DatasetID)
		}
		if dataset.SpaceID == "crypto" {
			switch dataset.DatasetID {
			case "dataset_binance_spot_symbols", "dataset_binance_spot_kline_1m", "dataset_spot_kline_1h":
				require.Equal(t, "spot", dataset.Attributes["market_type"], dataset.DatasetID)
			case "dataset_binance_swap_symbols", "dataset_perpetual_kline_1h":
				require.Equal(t, "swap", dataset.Attributes["market_type"], dataset.DatasetID)
			}
		}
	}
	for _, column := range seed.DatasetColumns {
		columnCount[column.SpaceID+"/"+column.DatasetID]++
	}
	for _, view := range seed.Views {
		require.LessOrEqual(t, utf8.RuneCountInString(view.Name), 10, view.SpaceID+"/"+view.ViewID)
		for _, datasetID := range view.DatasetIDs {
			viewCount[view.SpaceID+"/"+datasetID]++
		}
		if view.SpaceID == "crypto" && view.ViewID != "view_crypto_spot_kline_1m" && view.ViewID != "view_crypto_swap_kline_1m" {
			require.Contains(t, view.FilterJSON, `"freq":"1H"`, view.ViewID)
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
		"dataset_stockcn_instruments",
	}, datasetsBySpace["stockcn"])
	require.Equal(t, []string{"dataset_binance_spot_kline_1m", "dataset_binance_spot_symbols", "dataset_binance_swap_kline_1m", "dataset_binance_swap_symbols", "dataset_perpetual_kline_1h", "dataset_spot_kline_1h"}, datasetsBySpace["crypto"])
}

func TestDefaultSetupBundleDefinesStockCNInstrumentsLikeSymbolDatasets(t *testing.T) {
	seed, err := loadMetadataSeed(defaultSetupBundlePath("metadata.yaml"))
	require.NoError(t, err)

	var instrumentDataset *seedDataset
	for i := range seed.Datasets {
		dataset := &seed.Datasets[i]
		if dataset.SpaceID == "stockcn" && dataset.DatasetID == "dataset_stockcn_instruments" {
			instrumentDataset = dataset
			break
		}
	}
	require.NotNil(t, instrumentDataset)
	require.Equal(t, "stockcn", instrumentDataset.DataSourceID)
	require.Equal(t, "record", instrumentDataset.DataKind)
	require.Equal(t, "storage-node-0", instrumentDataset.DataNodeID)
	require.Equal(t, "0", instrumentDataset.KeepDuration)
	require.Equal(t, "disabled", instrumentDataset.Status)
	require.Equal(t, "wide_common_metrics", instrumentDataset.Attributes["storage_model"])

	type columnContract struct {
		valueType string
		required  bool
	}
	wantColumns := map[string]columnContract{
		"security_code":     {valueType: "string", required: true},
		"provider_symbol":   {valueType: "string", required: true},
		"exchange":          {valueType: "string", required: true},
		"instrument_name":   {valueType: "string"},
		"instrument_status": {valueType: "string"},
		"snapshot_id":       {valueType: "string", required: true},
		"source_provider":   {valueType: "string", required: true},
		"fetched_at":        {valueType: "time", required: true},
	}
	gotColumns := make(map[string]columnContract)
	for _, column := range seed.DatasetColumns {
		if column.SpaceID == "stockcn" && column.DatasetID == "dataset_stockcn_instruments" {
			gotColumns[column.ColumnName] = columnContract{valueType: column.ValueType, required: column.Required}
		}
	}
	require.Equal(t, wantColumns, gotColumns)
}

func TestDefaultSetupBundleUsesOnlyFixedFiles(t *testing.T) {
	for _, name := range []string{
		"metadata.yaml",
		"collector-rules.yaml",
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
