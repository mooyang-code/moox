package command

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuantInitialSeedUsesUnifiedCryptoMarket(t *testing.T) {
	seed, err := loadMetadataSeed(filepath.Join("..", "..", "..", "..", "examples", "metadata-quant-initial.seed.yaml"))
	require.NoError(t, err)
	spaceIDs := make([]string, 0, len(seed.Spaces))
	for _, space := range seed.Spaces {
		spaceIDs = append(spaceIDs, space.SpaceID)
	}
	require.Equal(t, []string{"stock_cn", "stock_hk", "stock_us", "crypto"}, spaceIDs)

	var dataSourceIDs []string
	var datasetIDs []string
	var viewIDs []string
	for _, item := range seed.DataSources {
		if item.SpaceID == "crypto" {
			dataSourceIDs = append(dataSourceIDs, item.DataSourceID)
		}
	}
	for _, item := range seed.Datasets {
		if item.SpaceID == "crypto" {
			datasetIDs = append(datasetIDs, item.DatasetID)
		}
	}
	for _, item := range seed.Views {
		if item.SpaceID == "crypto" {
			viewIDs = append(viewIDs, item.ViewID)
		}
	}

	require.ElementsMatch(t, []string{"binance", "okx"}, dataSourceIDs)
	require.ElementsMatch(t, []string{
		"binance_spot_kline_1h",
		"binance_perpetual_kline_1h",
		"okx_spot_kline_1h",
		"okx_perpetual_kline_1h",
	}, datasetIDs)
	require.ElementsMatch(t, []string{
		"binance_spot_1h_view",
		"binance_perpetual_1h_view",
		"okx_spot_1h_view",
		"okx_perpetual_1h_view",
	}, viewIDs)
	for _, item := range seed.Datasets {
		require.Equal(t, "storage-node-0", item.DataNodeID, item.DatasetID)
		require.NotEmpty(t, item.KeepDuration, item.DatasetID)
		require.Equal(t, "disabled", item.Status, item.DatasetID)
	}
}
