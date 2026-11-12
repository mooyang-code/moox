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

	require.ElementsMatch(t, []string{"crypto", "binance", "okx"}, dataSourceIDs)

	var stockCNSources []string
	for _, item := range seed.DataSources {
		if item.SpaceID == "stockcn" {
			stockCNSources = append(stockCNSources, item.DataSourceID)
		}
	}
	require.ElementsMatch(t, []string{
		"stockcn", "quantclass_stock", "eastmoney", "tdx", "tencent", "sina", "baidu", "ths", "market_data",
	}, stockCNSources)
	// A 股采集结果由采集任务派生（dataset_<task_id>），种子里没有 A 股行情 Dataset 与 View。
	for _, item := range seed.Views {
		require.NotEqual(t, "stockcn", item.SpaceID, item.ViewID)
	}
	// Crypto results are task-owned Datasets that Collector creates.
	require.Empty(t, datasetIDs)
	require.Empty(t, viewIDs)
	for _, item := range seed.Views {
		if item.SpaceID == "crypto" {
			require.Equal(t, []string{"subject_id", "freq", "data_time", "series_tag"}, item.GrainKeys, item.ViewID)
		}
	}
}
