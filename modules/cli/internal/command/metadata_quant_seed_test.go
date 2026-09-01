package command

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultMetadataCoversCanonicalMarkets(t *testing.T) {
	seed, err := loadMetadataSeed(defaultSetupBundlePath("metadata.yaml"))
	require.NoError(t, err)
	spaceIDs := make([]string, 0, len(seed.Spaces))
	for _, space := range seed.Spaces {
		spaceIDs = append(spaceIDs, space.SpaceID)
	}
	require.Equal(t, []string{"stock_cn", "stock_hk", "stock_us", "crypto", "moox_system"}, spaceIDs)

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
	require.ElementsMatch(t, []string{
		"binance_spot_symbols",
		"binance_swap_symbols",
		"binance_spot_kline_1m",
		"binance_swap_kline_1m",
		"spot_kline_1h",
		"perpetual_kline_1h",
	}, datasetIDs)
	require.ElementsMatch(t, []string{
		"binance_spot_kline_1m_view",
		"binance_swap_kline_1m_view",
		"spot_kline_1h_view",
		"perpetual_kline_1h_view",
	}, viewIDs)
	for _, item := range seed.Datasets {
		require.Equal(t, "storage-node-0", item.DataNodeID, item.DatasetID)
		require.NotEmpty(t, item.KeepDuration, item.DatasetID)
		require.Equal(t, "disabled", item.Status, item.DatasetID)
		if item.SpaceID == "moox_system" && item.DatasetID == "moox_service_metrics" {
			require.Equal(t, "24h", item.KeepDuration)
		}
		if item.SpaceID == "crypto" && item.DatasetID != "binance_spot_symbols" && item.DatasetID != "binance_swap_symbols" && item.DatasetID != "binance_spot_kline_1m" && item.DatasetID != "binance_swap_kline_1m" {
			require.Equal(t, "crypto", item.DataSourceID, item.DatasetID)
		}
	}
	for _, item := range seed.Views {
		if item.SpaceID == "crypto" {
			require.Equal(t, []string{"subject_id", "freq", "data_time", "series_tag"}, item.GrainKeys, item.ViewID)
		}
		if item.SpaceID == "moox_system" && item.ViewID == "moox_service_metrics_view" {
			require.Equal(t, "24h", item.KeepDuration)
		}
	}
}

func TestQuantSampleCSVUsesSharedDatasetAndSeriesTag(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "examples", "data", "kline")
	cases := []struct {
		path      string
		datasetID string
		seriesTag string
		frequency string
	}{
		{"crypto/binance_spot_kline_1h.csv", "spot_kline_1h", "venue:binance", "1H"},
		{"crypto/binance_perpetual_kline_1h.csv", "perpetual_kline_1h", "venue:binance", "1H"},
		{"crypto/okx_spot_kline_1h.csv", "spot_kline_1h", "venue:okx", "1H"},
		{"stock_cn/stock_cn_kline_1d.csv", "stock_cn_kline", "", "1d"},
		{"stock_cn/stock_cn_kline_1h.csv", "stock_cn_kline", "", "1H"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			file, err := os.Open(filepath.Join(root, tc.path))
			require.NoError(t, err)
			defer file.Close()
			rows, err := csv.NewReader(file).ReadAll()
			require.NoError(t, err)
			require.Greater(t, len(rows), 1)
			require.Equal(t,
				[]string{"space_id", "dataset_id", "subject_id", "freq", "data_time", "series_tag"},
				rows[0][:6],
			)
			for _, row := range rows[1:] {
				require.Equal(t, tc.datasetID, row[1])
				require.Equal(t, tc.seriesTag, row[5])
				require.Equal(t, tc.frequency, row[3])
			}
		})
	}
}
