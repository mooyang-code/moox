package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 源是统一数据集（同一数据源的现货加永续）：合成源参数是 multi，每个市场一条源序列；单市场的源保持原样。
func TestResampleSourceOfUnifiedDataset(t *testing.T) {
	db := openCollectorTestStore(t)
	ctx := context.Background()
	tags := map[string]*storagepb.Tag{
		"binance_spot": {TagId: "binance_spot", Attributes: map[string]string{"sync.source": "binance", "market_type": "spot"}},
		"binance_swap": {TagId: "binance_swap", Attributes: map[string]string{"sync.source": "binance", "market_type": "swap"}},
		"okx_swap":     {TagId: "okx_swap", Attributes: map[string]string{"sync.source": "okx", "market_type": "swap"}},
	}
	for _, task := range []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "unified", TaskName: "unified", DataType: "kline", Enabled: true, TagIDs: []string{"binance_spot", "binance_swap"},
			CollectParams: `{"target_dataset_id":"bars_unified","frequency":"1m"}`, ResultDatasetID: "bars_unified", ResultViewID: "view_unified"},
		{SpaceID: "crypto", TaskID: "swap-only", TaskName: "swap-only", DataType: "kline", Enabled: true, TagIDs: []string{"binance_swap"},
			CollectParams: `{"target_dataset_id":"bars_swap","frequency":"1m"}`, ResultDatasetID: "bars_swap", ResultViewID: "view_swap"},
		{SpaceID: "crypto", TaskID: "mixed", TaskName: "mixed", DataType: "kline", Enabled: true, TagIDs: []string{"binance_spot", "okx_swap"},
			CollectParams: `{"target_dataset_id":"bars_mixed","frequency":"1m"}`, ResultDatasetID: "bars_mixed", ResultViewID: "view_mixed"},
	} {
		require.NoError(t, db.Tasks().Create(ctx, task))
	}
	service := &Service{taskRepo: db.Tasks(), datasetSrc: tagValidationSource{validationDatasetSource: validationDatasetSource{
		"bars_unified": storagesource.DatasetInfo{SubjectTags: []string{"binance_spot", "binance_swap"}},
		"bars_swap":    storagesource.DatasetInfo{SubjectTags: []string{"binance_swap"}},
	}, tags: tags}}
	service.sourceIDResolver = func(provider, instrumentType string) string {
		if provider == "binance" && instrumentType == "perpetual" {
			return "swap_http"
		}
		if provider == "binance" {
			return "spot_http"
		}
		return ""
	}

	unified, err := service.resampleSourceOf(ctx, "crypto", "unified")
	require.NoError(t, err)
	require.Equal(t, "multi", unified.MarketType)
	require.Empty(t, unified.SeriesTag)
	require.Len(t, unified.SeriesTags, 2, "每个市场一条源序列")
	require.Equal(t, "binance", unified.Provider)

	single, err := service.resampleSourceOf(ctx, "crypto", "swap-only")
	require.NoError(t, err)
	require.Equal(t, "swap", single.MarketType)
	require.NotEmpty(t, single.SeriesTag)
	require.Empty(t, single.SeriesTags, "单市场的源仍是 source_series_tag 一项")

	_, err = service.resampleSourceOf(ctx, "crypto", "mixed")
	require.ErrorContains(t, err, "现货加永续")

	// 推导出的参数：统一源写 source_series_tags，单市场源写 source_series_tag。
	derived := domain.CollectionTask{SpaceID: "crypto", TaskID: "derived", DataType: "kline_resample",
		CollectParams: `{"source_task_id":"unified","target_frequency":"5m"}`}
	require.NoError(t, service.deriveResampleSource(ctx, &derived))
	params, err := domain.ParseCollectParams(derived.CollectParams, "", "", "kline_resample")
	require.NoError(t, err)
	require.Equal(t, "multi", params.MarketType)
	require.Empty(t, params.SourceSeriesTag)
	require.Equal(t, unified.SeriesTags, params.SourceSeriesTags)
	derivedSingle := domain.CollectionTask{SpaceID: "crypto", TaskID: "derived-single", DataType: "kline_resample",
		CollectParams: `{"source_task_id":"swap-only","target_frequency":"5m"}`}
	require.NoError(t, service.deriveResampleSource(ctx, &derivedSingle))
	params, err = domain.ParseCollectParams(derivedSingle.CollectParams, "", "", "kline_resample")
	require.NoError(t, err)
	require.Equal(t, single.SeriesTag, params.SourceSeriesTag)
	require.Empty(t, params.SourceSeriesTags)
}
