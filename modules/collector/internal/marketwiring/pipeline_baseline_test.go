package marketwiring

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// 比对测试（路由与流水线通用化）：各市场各品种装出来的 K 线流水线——候选链、来源绑定、结算延迟、熔断阈值、
// 每个数据源的限速——必须与 testdata 里记录的逐项相同。记录文件是在合并流水线构造函数之前的代码上生成的；
// 要重新生成时设 MOOX_UPDATE_GOLDEN=1。
const pipelineBaselineFile = "testdata/pipeline_baseline.json"

// baselinePipeline 按请求里的（市场，品种，频率，数据源，source_id，限速）装一条流水线，与 Handler 的装配相同。
func baselinePipeline(marketID string, instrument marketdata.InstrumentType, frequency, provider, sourceID string, limits *marketfetch.FeedLimits) (*marketfetch.KlinePipeline, error) {
	switch {
	case marketID == "stockcn" && instrument == marketdata.InstrumentEquity:
		return NewStockKlinePipeline(timerHandlerStorage{}, frequency)
	case marketID == "crypto":
		return NewCryptoKlinePipeline(timerHandlerStorage{}, instrument, limits)
	}
	return NewMarketKlinePipeline(timerHandlerStorage{}, marketID, instrument, provider, sourceID)
}

type baselineSource struct {
	SourceID          string                     `json:"source_id"`
	RateLimit         marketdata.RateLimitPolicy `json:"rate_limit"`
	MaxBarsPerRequest int                        `json:"max_bars_per_request"`
}

type baselinePipelineView struct {
	CandidateChain   []string                  `json:"candidate_chain"`
	RouteID          string                    `json:"route_id"`
	SpaceID          string                    `json:"space_id"`
	MarketID         string                    `json:"market_id"`
	InstrumentType   string                    `json:"instrument_type"`
	DatasetID        string                    `json:"dataset_id"`
	SourceID         string                    `json:"source_id"`
	AutoBindSource   bool                      `json:"auto_bind_source"`
	SeriesTag        string                    `json:"series_tag"`
	SettleDelayMS    int64                     `json:"settle_delay_ms"`
	BreakerThreshold int                       `json:"breaker_threshold"`
	FieldSources     int                       `json:"field_sources"`
	ExtraFieldIDs    []string                  `json:"extra_field_ids"`
	Sources          map[string]baselineSource `json:"sources"`
	Error            string                    `json:"error,omitempty"`
}

func TestPipelineBaseline(t *testing.T) {
	t.Setenv("MOOX_FETCH_FEED_CONCURRENCY", "4")
	t.Setenv("MOOX_FETCH_HTTP_MAX_ATTEMPTS", "")
	limits := &marketfetch.FeedLimits{RPS: 20, Burst: 40, FeedConcurrency: 8, RequestTimeoutMS: 2500, StorageTimeoutMS: 2000}
	cases := []struct {
		name       string
		marketID   string
		instrument marketdata.InstrumentType
		frequency  string
		provider   string
		sourceID   string
		limits     *marketfetch.FeedLimits
	}{
		{"stockcn/equity/1m", "stockcn", marketdata.InstrumentEquity, "1m", "", "", limits},
		{"stockcn/equity/5m", "stockcn", marketdata.InstrumentEquity, "5m", "", "", limits},
		{"stockcn/equity/1d", "stockcn", marketdata.InstrumentEquity, "1d", "", "", nil},
		{"stockcn/equity/15m（没有这个频率的路由）", "stockcn", marketdata.InstrumentEquity, "15m", "", "", nil},
		{"crypto/spot/带限速", "crypto", marketdata.InstrumentSpot, "1m", "binance", "spot_http", limits},
		{"crypto/swap/带限速", "crypto", marketdata.InstrumentSwap, "1h", "binance", "swap_http", limits},
		{"crypto/spot/不带限速", "crypto", marketdata.InstrumentSpot, "1m", "binance", "spot_http", nil},
		{"crypto/swap/不带限速", "crypto", marketdata.InstrumentSwap, "1m", "binance", "swap_http", nil},
		{"stockcn/index/eastmoney", "stockcn", marketdata.InstrumentIndex, "5m", "eastmoney", "index_http", nil},
		{"stockcn/index/cni（source_id 取登记值）", "stockcn", marketdata.InstrumentIndex, "1d", "cni", "", nil},
		{"stockcn/convertible_bond/sina", "stockcn", marketdata.InstrumentConvertibleBond, "5m", "sina", "convertible_bond_http", nil},
	}
	got := map[string]baselinePipelineView{}
	for _, item := range cases {
		pipeline, err := baselinePipeline(item.marketID, item.instrument, item.frequency, item.provider, item.sourceID, item.limits)
		if err != nil {
			got[item.name] = baselinePipelineView{Error: err.Error()}
			continue
		}
		view := baselinePipelineView{
			CandidateChain: pipeline.CandidateChain, RouteID: pipeline.RouteID, SpaceID: pipeline.SpaceID, MarketID: pipeline.MarketID,
			InstrumentType: string(pipeline.InstrumentType), DatasetID: pipeline.DatasetID, SourceID: pipeline.SourceID, AutoBindSource: pipeline.AutoBindSource,
			SeriesTag: pipeline.SeriesTag, SettleDelayMS: pipeline.SettleDelay.Milliseconds(), BreakerThreshold: pipeline.Router.BreakerThreshold(),
			FieldSources: len(pipeline.Fields), ExtraFieldIDs: pipeline.ExtraFieldIDs, Sources: map[string]baselineSource{},
		}
		session := pipeline.Router.NewSession()
		for _, provider := range pipeline.CandidateChain {
			spec, err := session.KlineSpec(provider)
			require.NoError(t, err, item.name)
			source := baselineSource{RateLimit: spec.RateLimit, MaxBarsPerRequest: spec.MaxBarsPerRequest}
			if registered, err := pipeline.Router.Provider(provider); err == nil {
				source.SourceID = registered.Descriptor().SourceID
			}
			view.Sources[provider] = source
		}
		got[item.name] = view
	}
	raw, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	raw = append(raw, '\n')
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(pipelineBaselineFile), 0o755))
		require.NoError(t, os.WriteFile(pipelineBaselineFile, raw, 0o644))
		return
	}
	want, err := os.ReadFile(pipelineBaselineFile)
	require.NoError(t, err)
	require.Equal(t, string(want), string(raw), "流水线的装配结果与改造前记录的不一致")
	// 抽查：币安的限速来自请求带的限速，A 股的结算延迟是 800 毫秒。
	require.Equal(t, float64(20), got["crypto/spot/带限速"].Sources["binance"].RateLimit.RequestsPerSecond)
	require.Equal(t, 8, got["crypto/swap/带限速"].Sources["binance"].RateLimit.MaxConcurrent)
	require.EqualValues(t, 800, got["stockcn/equity/5m"].SettleDelayMS)
}
