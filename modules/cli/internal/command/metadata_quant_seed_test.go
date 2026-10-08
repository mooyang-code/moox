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
	require.Equal(t, []string{"stockcn", "stockhk", "stockus", "crypto", "mooxsys"}, spaceIDs)

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
	var stockCNViews []string
	for _, item := range seed.Views {
		if item.SpaceID == "stockcn" {
			stockCNViews = append(stockCNViews, item.ViewID)
		}
	}
	require.ElementsMatch(t, []string{
		"view_stockcn_equity_kline_1m", "view_stockcn_index_kline_1d", "view_stockcn_bond_kline_1m",
	}, stockCNViews)
	// Crypto results are task-owned Datasets that Collector creates.
	require.Empty(t, datasetIDs)
	var stockKline seedDataset
	foundStockKline := false
	for _, item := range seed.Datasets {
		if item.SpaceID == "stockcn" && item.DatasetID == "dataset_stockcn_equity_kline_1m" {
			stockKline = item
			foundStockKline = true
			break
		}
	}
	require.True(t, foundStockKline)
	require.Equal(t, "1m", stockKline.Freq)
	require.Empty(t, viewIDs)
	for _, item := range seed.Datasets {
		require.Equal(t, "storage-node-0", item.DataNodeID, item.DatasetID)
		require.NotEmpty(t, item.KeepDuration, item.DatasetID)
		require.Equal(t, "disabled", item.Status, item.DatasetID)
		if item.SpaceID == "mooxsys" && item.DatasetID == "dataset_mooxsys_service_metrics" {
			require.Equal(t, "24h", item.KeepDuration)
		}
	}
	for _, item := range seed.Views {
		if item.SpaceID == "crypto" {
			require.Equal(t, []string{"subject_id", "freq", "data_time", "series_tag"}, item.GrainKeys, item.ViewID)
		}
		if item.SpaceID == "mooxsys" && item.ViewID == "view_mooxsys_service_metrics" {
			require.Equal(t, "24h", item.KeepDuration)
		}
	}
}

func TestDefaultMetadataDefinesStrictStockCNKlineColumns(t *testing.T) {
	seed, err := loadMetadataSeed(defaultSetupBundlePath("metadata.yaml"))
	require.NoError(t, err)

	var columns []string
	for _, item := range seed.DatasetColumns {
		if item.SpaceID == "stockcn" && item.DatasetID == "dataset_stockcn_equity_kline_1m" {
			columns = append(columns, item.ColumnName)
		}
	}
	require.Equal(t, []string{
		"open",
		"high",
		"low",
		"close",
		"volume",
		"amount",
		"instrument_name",
		"trade_date",
		"close_time",
		"volume_unit",
		"amount_unit",
		"amount_quality",
		"provider_symbol",
		"provider_timestamp",
		"fetched_at",
		"request_id",
		"route_id",
		"route_rank",
		"source_provider",
		"quality_status",
		"provider_id",
		"source_id",
	}, columns)
}
