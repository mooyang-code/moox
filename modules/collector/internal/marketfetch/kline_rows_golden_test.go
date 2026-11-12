package marketfetch

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 比对测试：同一批输入经 K 线流水线写入 Storage 的行，必须与 testdata 里记录的逐字段相同。
// 记录文件是在市场通用化改造之前的代码上生成的；要重新生成时设 MOOX_UPDATE_GOLDEN=1。
const klineRowsGoldenFile = "testdata/kline_rows.golden"

// goldenProvider 是比对测试用的数据源：按请求返回固定的 K 线，并把流水线传给数据源的代码、交易所、source_id
// 记在 request_id 里，这样它们也进入比对。
type goldenProvider struct {
	id        string
	sourceID  string
	fail      map[string]bool
	estimated bool
}

func (p goldenProvider) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: p.id, SourceID: p.sourceID, DisplayName: p.id, Hosts: []string{p.id + ".test"}}
}

func (p goldenProvider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets: []string{"stockcn"}, Exchanges: []string{"XSHG", "XSHE", "XBSE"}, Frequencies: []string{"5m", "1d"},
		CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 100, TimestampMode: marketdata.TimestampModeOpen,
		RateLimit: marketdata.RateLimitPolicy{RequestsPerSecond: 1000, Burst: 1000, MaxConcurrent: 64, Cooldown: time.Second, RequestTimeout: time.Second},
		History:   marketdata.KlineHistoryCapability{SupportsArbitraryRange: true},
	}
}

func (p goldenProvider) FetchKlines(_ context.Context, req marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	if p.fail[req.SubjectID] {
		return nil, marketdata.ErrProtocol
	}
	step, count := 5*time.Minute, 2
	if req.Frequency == "1d" {
		step, count = 24*time.Hour, 1
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(req.SubjectID))
	seed := float64(hash.Sum32()%9000)/100 + 5
	bars := make([]marketdata.NormalizedKline, 0, count)
	for index := 0; index < count; index++ {
		start := req.StartTime.Add(time.Duration(index) * step)
		price := seed + float64(index)*0.01
		volume := float64(1000 * (index + 1))
		bars = append(bars, marketdata.NormalizedKline{
			SubjectID: req.SubjectID, ProviderID: p.id, SourceID: req.SourceID, ProviderSymbol: req.ProviderSymbol, Frequency: req.Frequency,
			BarStart: start, BarEnd: start.Add(step), Open: price, High: price + 0.05, Low: price - 0.05, Close: price + 0.02,
			Volume: volume, Amount: volume * price, AmountEstimated: p.estimated,
			ProviderTimestamp: start.Add(step), FetchedAt: start.Add(step + time.Second),
			RequestID: strings.Join([]string{req.RequestID, string(req.ExchangeID), req.SourceID, req.ProviderSymbol}, "|"),
		})
	}
	return bars, nil
}

func goldenSubjects() []string {
	subjects := make([]string, 0, 45)
	for index := 0; index < 20; index++ {
		subjects = append(subjects, fmt.Sprintf("6000%02d.XSHG", index))
	}
	for index := 1; index <= 20; index++ {
		subjects = append(subjects, fmt.Sprintf("0000%02d.XSHE", index))
	}
	return append(subjects, "300750.XSHE", "688981.XSHG", "430047.XBSE", "830799.XBSE", "920002.XBSE")
}

func goldenStockPipeline(t *testing.T, frequency string, now time.Time, storage Storage) *KlinePipeline {
	t.Helper()
	route, found, err := LoadRoute(StockCNSpaceID)
	require.NoError(t, err)
	require.True(t, found)
	kline, err := route.Kline(frequency)
	require.NoError(t, err)
	chain := kline.ProviderChain()
	// 600001 只有通达信失败（回落到下一家，行里记 fallback）；000002 一路失败到候选链最后一家（5m 是腾讯，成交额是估算值）。
	failing := map[string]map[string]bool{"tdx": {"600001.XSHG": true}}
	for _, id := range chain[:len(chain)-1] {
		if failing[id] == nil {
			failing[id] = map[string]bool{}
		}
		failing[id]["000002.XSHE"] = true
	}
	registry := marketdata.NewRegistry()
	for _, source := range kline.Sources() {
		require.NoError(t, registry.Register(goldenProvider{id: source.Provider, sourceID: source.SourceID, fail: failing[source.Provider], estimated: source.Provider == "tencent"}))
	}
	router, err := marketdata.NewRouter(registry, len(chain)*klineProviderAttemptBudget*8, pipelineClock{now}, func(time.Duration) {})
	require.NoError(t, err)
	return &KlinePipeline{
		Router: router, Storage: storage, CandidateChain: chain, AutoBindSource: true,
		SpaceID: StockCNSpaceID, MarketID: StockCNSpaceID, InstrumentType: marketdata.InstrumentEquity,
		SettleDelay: 800 * time.Millisecond, Now: func() time.Time { return now },
	}
}

func goldenRealtimeRows(t *testing.T, frequency string, now, start time.Time) []*storagepb.RowFieldUpsert {
	t.Helper()
	storage := &pipelineStorageWithInstrumentNames{names: map[string]string{"600000.XSHG": "浦发银行", "000001.XSHE": "平安银行"}}
	pipeline := goldenStockPipeline(t, frequency, now, storage)
	items := make([]domain.CollectionItem, 0, 45)
	for _, subject := range goldenSubjects() {
		items = append(items, domain.CollectionItem{
			SubjectID: subject, Symbol: subject, MarketType: "equity", DataType: "kline", DatasetID: testStockDatasetID,
			Frequency: frequency, StartTime: start.Format(time.RFC3339),
		})
	}
	payload, err := pipeline.Execute(context.Background(), Request{
		BatchID: "golden-" + frequency, BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: testStockDatasetID,
		Frequency: frequency, MarketType: "equity", RequestID: "golden-request-" + frequency, Concurrency: 8, Items: items,
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	return storage.rows
}

func dumpRows(title string, rows []*storagepb.RowFieldUpsert) string {
	var out strings.Builder
	fmt.Fprintf(&out, "## %s（%d 行）\n", title, len(rows))
	for _, row := range rows {
		series := row.GetKey().GetTimeSeries()
		fmt.Fprintf(&out, "space=%s dataset=%s subject=%s freq=%s data_time=%s series_tag=%s\n",
			row.GetKey().GetSpaceId(), row.GetKey().GetDatasetId(), series.GetSubjectId(), series.GetFreq(), series.GetDataTime(), series.GetSeriesTag())
		for _, field := range row.GetFields() {
			switch value := field.GetValue().GetValue().(type) {
			case *storagepb.TypedValue_DoubleValue:
				fmt.Fprintf(&out, "  %s double %v\n", field.GetFieldId(), value.DoubleValue)
			case *storagepb.TypedValue_IntValue:
				fmt.Fprintf(&out, "  %s int %d\n", field.GetFieldId(), value.IntValue)
			case *storagepb.TypedValue_StringValue:
				fmt.Fprintf(&out, "  %s string %q\n", field.GetFieldId(), value.StringValue)
			case *storagepb.TypedValue_TimeValue:
				fmt.Fprintf(&out, "  %s time %s\n", field.GetFieldId(), value.TimeValue)
			default:
				fmt.Fprintf(&out, "  %s unknown %v\n", field.GetFieldId(), field.GetValue())
			}
		}
	}
	return out.String()
}

func TestKlineRowsMatchGolden(t *testing.T) {
	var out strings.Builder

	// A 股 5m 实时批：45 只标的（超过普通实时批 40 只的上限），沪深北三个交易所，来源按路由分配。
	out.WriteString(dumpRows("stockcn equity 5m 实时批", goldenRealtimeRows(t, "5m",
		time.Date(2026, 11, 5, 1, 40, 1, 0, time.UTC), time.Date(2026, 11, 5, 1, 30, 0, 0, time.UTC))))

	// A 股 1d 实时批：交易日 11-05 的日线在 11-06 00:00 UTC 之后采集。
	out.WriteString(dumpRows("stockcn equity 1d 实时批", goldenRealtimeRows(t, "1d",
		time.Date(2026, 11, 6, 0, 0, 5, 0, time.UTC), time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))))

	// A 股 5m 补采：单只标的、只写选中的字段。
	backfillNow := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	backfillStorage := &pipelineStorage{}
	backfill := goldenStockPipeline(t, "5m", backfillNow, backfillStorage)
	payload, err := backfill.Execute(context.Background(), Request{
		BatchID: "golden-backfill", BatchKind: domain.BatchKindBackfill, SpaceID: StockCNSpaceID, DatasetID: testStockDatasetID,
		Frequency: "5m", MarketType: "equity", RequestID: "golden-request-backfill",
		Items: []domain.CollectionItem{{
			SubjectID: "600519.XSHG", Symbol: "sh600519", MarketType: "equity", DataType: "kline", DatasetID: testStockDatasetID, Frequency: "5m",
			StartTime: "2026-11-05T02:00:00Z", EndTime: "2026-11-05T02:10:00Z", BarLimit: 2, OutputFields: []string{"close", "volume", "amount", "amount_unit", "trade_date", "source_provider"},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus())
	out.WriteString(dumpRows("stockcn equity 5m 补采（选字段）", backfillStorage.rows))

	// 直接构造行：A 股指数、可转债的普通行，以及 crypto 现货、永续的行。
	period := time.Date(2026, 11, 5, 1, 30, 0, 0, time.UTC)
	bar := func(subject, provider, source, symbol string) marketdata.NormalizedKline {
		return marketdata.NormalizedKline{
			SubjectID: subject, ProviderID: provider, SourceID: source, ProviderSymbol: symbol, Frequency: "5m",
			BarStart: period, BarEnd: period.Add(5 * time.Minute), Open: 10, High: 10.5, Low: 9.5, Close: 10.2,
			Volume: 12345, Amount: 125919, TradeCount: 678,
			ProviderTimestamp: period.Add(5 * time.Minute), FetchedAt: period.Add(6 * time.Minute), RequestID: "golden-direct",
			Extra: map[string]marketdata.FieldValue{"funding_rate": {Type: marketdata.FieldTypeDouble, Double: 0.0001}},
		}
	}
	direct := []struct {
		title      string
		committer  KlineCommitter
		req        Request
		bar        marketdata.NormalizedKline
		routeID    string
		routeRank  int
		instrument string
	}{
		{"stockcn index 普通行", KlineCommitter{SpaceID: "stockcn", MarketID: "stockcn", InstrumentType: marketdata.InstrumentIndex},
			Request{SpaceID: "stockcn", DatasetID: "dataset_index", Frequency: "5m", MarketType: "index", SourceID: "index_http"},
			bar("000300.XSHG", "eastmoney", "index_http", "sh000300"), "stockcn_index_kline", 1, ""},
		{"stockcn convertible_bond 普通行", KlineCommitter{SpaceID: "stockcn", MarketID: "stockcn", InstrumentType: marketdata.InstrumentConvertibleBond},
			Request{SpaceID: "stockcn", DatasetID: "dataset_bond", Frequency: "5m", MarketType: "convertible_bond", SourceID: "convertible_bond_http"},
			bar("113050.XSHG", "sina", "convertible_bond_http", "sh113050"), "stockcn_convertible_bond_kline", 1, ""},
		{"stockcn equity 未带品种的提交器", KlineCommitter{MarketID: "stockcn"},
			Request{SpaceID: "stockcn", DatasetID: "dataset_equity", Frequency: "5m", MarketType: "equity"},
			bar("600000.XSHG", "tdx", "normal_7709", "sh600000"), "stockcn_equity_kline_5m", 2, "浦发银行"},
		{"crypto spot 行", KlineCommitter{SpaceID: "crypto", MarketID: "crypto", InstrumentType: marketdata.InstrumentSpot},
			Request{SpaceID: "crypto", DatasetID: "dataset_spot", Frequency: "5m", MarketType: "spot", SourceID: "spot_http"},
			bar("btc-usdt-spot", "binance", "spot_http", "BTCUSDT"), "binance_spot_kline_5m", 1, ""},
		{"crypto swap 行", KlineCommitter{SpaceID: "crypto", MarketID: "crypto", InstrumentType: marketdata.InstrumentSwap},
			Request{SpaceID: "crypto", DatasetID: "dataset_swap", Frequency: "5m", MarketType: "swap", SourceID: "swap_http"},
			bar("BTC-USDT-SWAP", "binance", "swap_http", "BTCUSDT"), "binance_swap_kline_5m", 1, ""},
		{"crypto 指定序列标签", KlineCommitter{SpaceID: "crypto", MarketID: "crypto", InstrumentType: marketdata.InstrumentSpot, SeriesTag: "venue:custom"},
			Request{SpaceID: "crypto", DatasetID: "dataset_spot", Frequency: "5m", MarketType: "spot", SourceID: "spot_http"},
			bar("ETH-USDT-SPOT", "binance", "spot_http", "ETHUSDT"), "binance_spot_kline_5m", 1, ""},
	}
	for _, item := range direct {
		row, rowErr := item.committer.rowFor(item.bar, item.req, item.routeID, item.routeRank, item.instrument)
		require.NoError(t, rowErr, item.title)
		out.WriteString(dumpRows(item.title, []*storagepb.RowFieldUpsert{row}))
	}

	// 周期提交绑定用的序列标签。
	out.WriteString("## 取数项的序列标签\n")
	for _, item := range []struct {
		space string
		item  domain.CollectionItem
	}{
		{"stockcn", domain.CollectionItem{Provider: "eastmoney", SourceID: "stockcn_http", MarketType: "equity"}},
		{"stockcn", domain.CollectionItem{}},
		{"crypto", domain.CollectionItem{Provider: "binance", SourceID: "spot_http", MarketType: "spot"}},
		{"crypto", domain.CollectionItem{Provider: "binance", SourceID: "swap_http", MarketType: "swap"}},
		{"crypto", domain.CollectionItem{}},
	} {
		fmt.Fprintf(&out, "space=%s provider=%s source=%s market_type=%s => %q\n", item.space, item.item.Provider, item.item.SourceID, item.item.MarketType, collectionItemSeriesTag(item.space, item.item))
	}

	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(klineRowsGoldenFile), 0o755))
		require.NoError(t, os.WriteFile(klineRowsGoldenFile, []byte(out.String()), 0o644))
		return
	}
	want, err := os.ReadFile(klineRowsGoldenFile)
	require.NoError(t, err)
	require.Equal(t, string(want), out.String(), "K 线写入行与改造前记录的不一致")
}
