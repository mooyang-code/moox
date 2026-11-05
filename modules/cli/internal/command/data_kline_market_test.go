package command

import (
	"context"
	"io"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	unifiedSpotTag = "venue:binance|market:spot|source:spot_http"
	unifiedSwapTag = "venue:binance|market:swap|source:swap_http"
)

// 统一数据集：--market 用 market_series_tags 选序列；不给 --market 时仍用 series_tag。
func TestDataKlineMarketSelectsSeriesTag(t *testing.T) {
	cfg := testSkillDataAccessConfig()
	exchange := cfg.DataTypes["crypto"].Exchanges["binance"]
	exchange.MarketSeriesTags = map[string]string{"spot": unifiedSpotTag, "swap": unifiedSwapTag}
	cfg.DataTypes["crypto"].Exchanges["binance"] = exchange
	require.NoError(t, cfg.validate())

	run := func(args ...string) (*pb.TimeSeriesSelector, error) {
		reader := &fakeTimeSeriesReader{rsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}}
		cmd := newDataKlineGetCmd(dataKlineDeps{
			loadConfig: func(string) (dataAccessConfig, error) { return cfg, nil },
			newReader: func(context.Context, string, dataAccessConfig, string) (timeSeriesReader, io.Closer, error) {
				return reader, nil, nil
			},
		})
		cmd.SetArgs(append([]string{"--data-type", "crypto", "--symbol", "BTC-USDT"}, args...))
		if err := cmd.Execute(); err != nil {
			return nil, err
		}
		require.Len(t, reader.request.GetSelectors(), 1)
		return reader.request.GetSelectors()[0], nil
	}

	selector, err := run("--market", "swap")
	require.NoError(t, err)
	assert.Equal(t, unifiedSwapTag, selector.GetSeriesTag())
	selector, err = run("--market", " SPOT ")
	require.NoError(t, err)
	assert.Equal(t, unifiedSpotTag, selector.GetSeriesTag())
	selector, err = run()
	require.NoError(t, err)
	assert.Equal(t, unifiedSpotTag, selector.GetSeriesTag(), "不给 --market 时沿用 series_tag")

	_, err = run("--market", "futures")
	require.ErrorContains(t, err, "spot 或 swap")
}

func TestDataKlineMarketRequiresConfiguredSeriesTag(t *testing.T) {
	cfg := testSkillDataAccessConfig()
	_, err := cfg.resolveKline("crypto", "", "1m", "swap")
	require.ErrorContains(t, err, "market_series_tags")
}

func TestDataAccessConfigRejectsMismatchedMarketSeriesTag(t *testing.T) {
	cfg := testSkillDataAccessConfig()
	exchange := cfg.DataTypes["crypto"].Exchanges["binance"]
	exchange.MarketSeriesTags = map[string]string{"swap": unifiedSpotTag}
	cfg.DataTypes["crypto"].Exchanges["binance"] = exchange
	require.ErrorContains(t, cfg.validate(), "does not belong")
	exchange.MarketSeriesTags = map[string]string{"futures": unifiedSwapTag}
	cfg.DataTypes["crypto"].Exchanges["binance"] = exchange
	require.ErrorContains(t, cfg.validate(), "spot or swap")
}
