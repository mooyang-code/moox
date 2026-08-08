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
	items := []string{"..", "..", "..", "..", "examples", "setup", "default"}
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
	require.Equal(t, []string{"crypto_market", "stock_cn"}, business)
	require.Equal(t, []string{"moox_system"}, internal)

	byID := make(map[string]seedSpace, len(seed.Spaces))
	for _, space := range seed.Spaces {
		byID[space.SpaceID] = space
	}
	require.Equal(t, "CN", byID["stock_cn"].Market)
	require.Equal(t, "Asia/Shanghai", byID["stock_cn"].Timezone)
	require.Equal(t, "crypto", byID["crypto_market"].Market)
	require.Equal(t, "UTC", byID["crypto_market"].Timezone)
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
		if dataset.SpaceID == "crypto_market" && dataset.DatasetID != "binance_spot_symbols" && dataset.DatasetID != "binance_swap_symbols" && dataset.DatasetID != "binance_spot_kline_1m" {
			require.Equal(t, []string{"1H"}, dataset.Freqs, dataset.DatasetID)
		}
		if dataset.SpaceID == "crypto_market" {
			switch dataset.DatasetID {
			case "binance_spot_symbols", "binance_spot_kline_1m", "spot_kline_1h":
				require.Equal(t, "spot", dataset.Attributes["market_type"], dataset.DatasetID)
			case "binance_swap_symbols", "perpetual_kline_1h":
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
		if view.SpaceID == "crypto_market" && view.ViewID != "binance_spot_kline_1m_view" {
			require.Contains(t, view.FilterJSON, `"freq":"1H"`, view.ViewID)
		}
	}
	for _, column := range seed.ViewColumns {
		viewColumnCount[column.SpaceID+"/"+column.ViewID]++
	}
	for _, dataset := range seed.Datasets {
		key := dataset.SpaceID + "/" + dataset.DatasetID
		require.Positive(t, columnCount[key], "Dataset %s has no columns", key)
		if dataset.DatasetID != "binance_spot_symbols" && dataset.DatasetID != "binance_swap_symbols" {
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
		"financial_statement_metric",
		"financial_summary",
		"index_kline",
		"stock_kline",
	}, datasetsBySpace["stock_cn"])
	require.Equal(t, []string{"binance_spot_kline_1m", "binance_spot_symbols", "binance_swap_symbols", "perpetual_kline_1h", "spot_kline_1h"}, datasetsBySpace["crypto_market"])
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
