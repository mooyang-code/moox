package command

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultMetadataUsesUnifiedCryptoMarket(t *testing.T) {
	seed, err := loadMetadataSeed(defaultSetupBundlePath("metadata.yaml"))
	require.NoError(t, err)
	spaceIDs := make([]string, 0, len(seed.Spaces))
	for _, space := range seed.Spaces {
		spaceIDs = append(spaceIDs, space.SpaceID)
	}
	require.Equal(t, []string{"stockcn", "stockhk", "stockus", "crypto"}, spaceIDs)

	var dataSourceIDs []string
	var datasetIDs []string
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

	require.ElementsMatch(t, []string{"crypto", "binance", "okx"}, dataSourceIDs)

	var stockCNSources []string
	for _, item := range seed.DataSources {
		if item.SpaceID == "stockcn" {
			stockCNSources = append(stockCNSources, item.DataSourceID)
		}
	}
	require.ElementsMatch(t, []string{
		"stockcn", "quantclass_stock", "eastmoney", "tdx", "tencent", "sina", "ths", "market_data",
	}, stockCNSources)
	// Crypto results are task-owned Datasets that Collector creates.
	require.Empty(t, datasetIDs)
}
