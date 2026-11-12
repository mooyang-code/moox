package marketwiring

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	cloudfunctionpb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	hkeastmoney "github.com/mooyang-code/moox/modules/collector/internal/sources/stockhk/eastmoney"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

// 港股采集的端到端用例：假的东方财富服务加假的 Storage，其余都是真实装配（编进二进制的 config/markets/stockhk/、
// 行情源目录、标的规则、采集计划生成、补采）。定时请求写入一根、补采区间（含跨午休）由 stockhk_kline_test.go 覆盖，
// 这里补 16:00 那一期、收市后整天重取、从采集计划到函数入参三段。

const hkE2EDataset = "dataset_hk_5m"

// hkRowStore 是假的 Storage：按（标的，K 线开始时刻）存最新的一行，同时能被完整性检查读回。
type hkRowStore struct {
	mu   sync.Mutex
	rows map[string]map[string]*storagepb.TypedValue
	// writes 是每个行键被写入的次数。
	writes map[string]int
}

func newHKRowStore() *hkRowStore {
	return &hkRowStore{rows: map[string]map[string]*storagepb.TypedValue{}, writes: map[string]int{}}
}

func hkRowKey(subject, dataTime string) string { return subject + "|" + dataTime }

func (s *hkRowStore) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		key := hkRowKey(row.GetKey().GetTimeSeries().GetSubjectId(), row.GetKey().GetTimeSeries().GetDataTime())
		s.rows[key] = rowFields(row)
		s.writes[key]++
	}
	return nil
}

func (s *hkRowStore) CommitTimeSeriesBatch(ctx context.Context, _ *storagepb.DatasetPeriodExpectation, rows []*storagepb.TimeSeriesBatchRow) error {
	upserts := make([]*storagepb.RowFieldUpsert, 0, len(rows))
	for _, row := range rows {
		upserts = append(upserts, row.GetRow())
	}
	return s.UpsertFields(ctx, upserts)
}

// ReadTimeSeriesRows 给完整性检查用：返回选中的标的在区间内已有的行。
func (s *hkRowStore) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetStartTime())
	end, _ := time.Parse(time.RFC3339, req.GetTimeRange().GetEndTime())
	rsp := &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, PageResult: &storagepb.PageResult{}}
	for _, selector := range req.GetSelectors() {
		for key, fields := range s.rows {
			subject, dataTime, _ := strings.Cut(key, "|")
			at, err := time.Parse(time.RFC3339, dataTime)
			if subject != selector.GetSubjectId() || err != nil || at.Before(start) || !at.Before(end) {
				continue
			}
			row := &storagepb.TimeSeriesRow{Key: &storagepb.TimeSeriesKey{SubjectId: subject, DataTime: dataTime}}
			for _, column := range req.GetColumnNames() {
				if value, ok := fields[column]; ok {
					row.Fields = append(row.Fields, &storagepb.FieldValue{FieldId: column, Value: value})
				}
			}
			rsp.Rows = append(rsp.Rows, row)
		}
	}
	return rsp, nil
}

// prices 返回一行的开高低收量额，用来比较两次写入的行情值是否相同（采集时间、请求标识每次都不同，不比）。
func (s *hkRowStore) prices(subject, dataTime string) [6]float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := s.rows[hkRowKey(subject, dataTime)]
	var out [6]float64
	for i, name := range []string{"open", "high", "low", "close", "volume", "amount"} {
		out[i] = fields[name].GetDoubleValue()
	}
	return out
}

func (s *hkRowStore) snapshot() map[string][6]float64 {
	s.mu.Lock()
	keys := make([]string, 0, len(s.rows))
	for key := range s.rows {
		keys = append(keys, key)
	}
	s.mu.Unlock()
	out := make(map[string][6]float64, len(keys))
	for _, key := range keys {
		subject, dataTime, _ := strings.Cut(key, "|")
		out[key] = s.prices(subject, dataTime)
	}
	return out
}

// 16:00 那一期（云函数在 16:00:05 取 15:55 开始的那根）。返回内容是 2026-11-12 实测到的两种：
//   - 16:00:02 取到的连续交易时段的值（第 1 段），开高低收合法，照常写入；
//   - 16:02:39 取到的 09988：收盘价 96.3 被换成收市竞价的参考价，高于这一根的最高价 93.75（第 2 段）。这只标的整次失败，
//     同一期里别的标的照常写入。
//
// 第 2 段的返回放在 16:00:05 这个时刻是构造的：实时那一期不会落在收市竞价期间，落在那里的是自动补采；流水线对两者的处理相同。
func TestStockHKCloseBarPeriod(t *testing.T) {
	target := hkTime("2026-11-12 15:55:00")
	bodies := map[string]string{
		"116.00005": `"2026-11-12 16:00,141.100,141.100,141.200,141.000,88000,12417320.000"`,
		"116.09988": `"2026-11-12 15:55,93.600,93.700,93.850,93.400,3871100,362398515.000","2026-11-12 16:00,93.700,93.600,93.750,93.500,2848000,266640075.000"`,
	}
	request := func() marketfetch.Request {
		req := marketfetch.Request{
			BatchID: "close", BatchKind: domain.BatchKindRealtime, SpaceID: "stockhk", MarketID: "stockhk", InstrumentType: "equity",
			DatasetID: hkE2EDataset, Frequency: "5m", MarketType: "equity", RequestID: "close", Concurrency: 2,
		}
		for _, code := range []string{"00005", "09988"} {
			req.Items = append(req.Items, domain.CollectionItem{
				SubjectID: code + ".XHKG", Symbol: code, Provider: "eastmoney", MarketType: "equity", DataType: "kline", DatasetID: hkE2EDataset, Frequency: "5m",
				StartTime: target.Format(time.RFC3339Nano), EndTime: target.Add(5 * time.Minute).Format(time.RFC3339Nano), BarLimit: 1,
			})
		}
		return req
	}
	serve := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"klines":[` + bodies[r.URL.Query().Get("secid")] + `]}}`))
	}

	storage := newHKRowStore()
	payload, err := stockHKPipeline(t, storage, hkTime("2026-11-12 16:00:05"), nil, serve).Execute(context.Background(), request())
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
	require.Len(t, storage.rows, 2, "两只标的各写 15:55 开始的那一根")
	require.Equal(t, [6]float64{93.7, 93.75, 93.5, 93.6, 2848000, 266640075}, storage.prices("09988.XHKG", "2026-11-12T07:55:00Z"), "第 1 段：连续交易时段的值")
	require.Equal(t, [6]float64{141.1, 141.2, 141, 141.1, 88000, 12417320}, storage.prices("00005.XHKG", "2026-11-12T07:55:00Z"))

	// 09988 换成第 2 段的返回：收盘价超出高低。
	bodies["116.09988"] = `"2026-11-12 15:55,93.600,93.700,93.850,93.400,3871100,362398515.000","2026-11-12 16:00,93.700,96.300,93.750,93.500,2848000,266640075.000"`
	storage = newHKRowStore()
	payload, err = stockHKPipeline(t, storage, hkTime("2026-11-12 16:00:05"), nil, serve).Execute(context.Background(), request())
	require.NoError(t, err)
	require.NotEqual(t, "succeeded", payload.GetStatus())
	require.Contains(t, payload.GetErrorSummary(), "high must be >= open/close/low")
	require.Len(t, storage.rows, 1, "09988 整次失败，一根都不写")
	require.Equal(t, [6]float64{141.1, 141.2, 141, 141.1, 88000, 12417320}, storage.prices("00005.XHKG", "2026-11-12T07:55:00Z"), "00005 照常写入")
	outcomes := map[string]bool{}
	for _, result := range payload.GetItems() {
		outcomes[result.GetSubjectId()] = result.GetErrorSummary() == ""
	}
	require.Equal(t, map[string]bool{"00005.XHKG": true, "09988.XHKG": false}, outcomes)
}

// hkFunctionInvoker 把补采分块的调用交给真实的处理器执行（云函数里的同一个入口），只把连网的东方财富换成假服务。
type hkFunctionInvoker struct {
	t       *testing.T
	handler *marketfetch.Handler
	mu      sync.Mutex
	calls   int
}

func (i *hkFunctionInvoker) Invoke(ctx context.Context, _, functionName string, event map[string]any, _ cloudfunctionpb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	i.mu.Lock()
	i.calls++
	i.mu.Unlock()
	data, _ := event["data"].(map[string]any)
	response, err := i.handler.HandleWithFunctionName(ctx, model.CloudFunctionEvent{Action: model.EventActionMarketFetch, Data: data}, functionName)
	if err != nil {
		return scfinvoker.InvocationResult{}, err
	}
	raw, err := json.Marshal(response)
	require.NoError(i.t, err)
	result := map[string]any{}
	require.NoError(i.t, json.Unmarshal(raw, &result))
	return scfinvoker.InvocationResult{Message: response.Message, Result: result}, nil
}

type hkBackfillFunctions struct{}

func (hkBackfillFunctions) EnsureBackfillFunction(context.Context, string) (string, error) {
	return "moox-fetch-plan-backfill-stockhk-equity-nanjing", nil
}

// 收市后整天重取，从“到点提交”到“行被覆盖”走一遍：
//  1. 库里先有当天的 66 根，16:00 那根是连续交易时段的值；
//  2. 16:15 那一轮提交强制补采作业，分块经云函数的处理器取数、写入；
//  3. 16:00 那根被覆盖成含收市竞价的值（成交量变大），其余 65 根重写后开高低收量额与原来相同。
//
// 样例是 00700 在 2026-11-11 整天的实际返回（16:00 那根是收市竞价并入后的最终值，成交量 2819000）。第 1 步里
// 16:00 那根“连续交易时段的值”是构造的（那天收市前没有取样），只把成交量、成交额改小，开高低收改成落在这一根的高低之内。
func TestStockHKRefetchAfterCloseOverwritesTheCloseBar(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "stockhk")
	final, err := os.ReadFile(filepath.Join("..", "sources", "stockhk", "eastmoney", "testdata", "kline_5m_00700_20261111.json"))
	require.NoError(t, err)
	const finalClose = `"2026-11-11 16:00,407.800,409.600,409.600,407.600,2819000,1153976836.000,`
	require.Equal(t, 1, bytes.Count(final, []byte(finalClose)))
	continuous := bytes.Replace(final, []byte(finalClose), []byte(`"2026-11-11 16:00,407.800,408.000,408.200,407.600,611200,249300000.000,`), 1)
	var body []byte
	var mu sync.Mutex
	serve := func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "116.00700", r.URL.Query().Get("secid"))
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(body)
	}
	setBody := func(value []byte) { mu.Lock(); body = value; mu.Unlock() }

	// 采集计划：真实的生成逻辑，一个数据集一只标的，2026-11-10 开盘起生效。
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	desired, err := marketfetch.BuildDesiredState(hkE2EPlanOptions(), []marketfetch.TaskGroup{hkE2EGroup("00700.XHKG")}, nil)
	require.NoError(t, err)
	content, _, err := marketfetch.BuildPlanContent(nil, nil, desired, func(string) (int64, error) { return hkTime("2026-11-10 09:30:00").Unix(), nil })
	require.NoError(t, err)
	raw, err := json.Marshal(content)
	require.NoError(t, err)
	record, err := db.FetchPlans().Create(context.Background(), "stockhk", string(raw), "{}")
	require.NoError(t, err)
	require.NoError(t, db.FetchPlans().Update(context.Background(), record.ID, store.FetchPlanApplied, "{}", ""))

	storage := newHKRowStore()
	// 16:02：15:55 开始的那根已过允许迟到时间（收盘后 90 秒），按缺口补采才会把它算进应有的根。
	now := hkTime("2026-11-11 16:02:00")
	clock := func() time.Time { return now }
	handler := NewHandler()
	handler.Now = clock
	handler.NewStorage = func(string) (marketfetch.Storage, error) { return storage, nil }
	handler.NewKlinePipeline = func(s marketfetch.Storage, market string, instrument marketdata.InstrumentType, frequency, provider string, limits *marketfetch.FeedLimits) (*marketfetch.KlinePipeline, error) {
		require.Equal(t, "stockhk", market)
		require.Equal(t, marketdata.InstrumentEquity, instrument)
		require.Equal(t, "5m", frequency)
		return stockHKPipeline(t, s, clock(), limits, serve), nil
	}
	invoker := &hkFunctionInvoker{t: t, handler: handler}
	checker := &klinecheck.Checker{Plans: db.FetchPlans(), Rows: storage, Absences: db.Backfill(), Now: clock, Calendar: markets.TradingBar}
	backfiller := &klinecheck.Backfiller{
		Checker: checker, Store: db.Backfill(), Plans: db.FetchPlans(), Invoker: invoker, BaseCtx: context.Background(), Now: clock,
		Functions:    func(string) klinecheck.FunctionProvider { return hkBackfillFunctions{} },
		CloseRefetch: markets.CloseRefetchDay,
	}
	ctx := context.Background()
	runJobs := func() {
		for i := 0; i < 3; i++ {
			require.NoError(t, backfiller.Tick(ctx, "stockhk"))
			backfiller.Wait()
		}
	}

	// 第 1 步：库里是空的，按缺口补采把当天 66 根写进去（16:00 那根是连续交易时段的值）。还没到重取的时刻，不提交重取。
	setBody(continuous)
	require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
	jobID, chunks, err := backfiller.Submit(ctx, klinecheck.JobRequest{SpaceID: "stockhk", From: hkTime("2026-11-11 00:00:00"), To: hkTime("2026-11-12 00:00:00")}, store.BackfillSourceManual)
	require.NoError(t, err)
	require.Equal(t, 2, chunks, "上午、下午各一块")
	runJobs()
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status, "%+v", status.Failed)
	before := storage.snapshot()
	require.Len(t, before, 66)
	require.Equal(t, [6]float64{407.8, 408.2, 407.6, 408, 611200, 249300000}, before[hkRowKey("00700.XHKG", "2026-11-11T07:55:00Z")])

	// 第 2 步：16:15 那一轮。东方财富这时返回的 16:00 那根已经并入收市竞价的成交。
	now = hkTime("2026-11-11 16:15:10")
	setBody(final)
	require.NoError(t, backfiller.RefetchAfterClose(ctx, "stockhk"))
	jobs, _, err := db.Backfill().ListJobs(ctx, "stockhk", store.BackfillJobFilter{}, 1, 10)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	refetch := jobs[0]
	require.Equal(t, store.BackfillKindForce, refetch.Kind)
	callsBefore := invoker.calls
	runJobs()
	status, err = backfiller.Status(ctx, refetch.JobID)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status, "%+v", status.Failed)
	require.Equal(t, 2, invoker.calls-callsBefore, "每只标的上午、下午各调用一次函数")

	// 第 3 步：只有 16:00 那根的行情值变了。
	after := storage.snapshot()
	require.Len(t, after, 66)
	var changed []string
	for key, value := range after {
		if value != before[key] {
			changed = append(changed, key)
		}
		require.Equal(t, 2, storage.writes[key], "%s：补采一次、重取一次", key)
	}
	sort.Strings(changed)
	require.Equal(t, []string{hkRowKey("00700.XHKG", "2026-11-11T07:55:00Z")}, changed)
	require.Equal(t, [6]float64{407.8, 409.6, 407.6, 409.6, 2819000, 1153976836}, after[changed[0]], "含收市竞价的最终值，成交量从 61.12 万股变成 281.9 万股")
}

func hkE2EPlanOptions() marketfetch.PlanOptions {
	// 与 Collector 装配采集计划时相同：函数内并发由港股的路由与东方财富的限速配置推导，每函数 25 只、收盘后等 5 秒是 market.yaml 的默认值。
	return marketfetch.PlanOptions{SpaceID: "stockhk", Markets: map[string][]string{"equity": {"ap-nanjing"}}, DefaultLatenessS: 90,
		SymbolsPerFunction: 25, Fetch: marketfetch.PlanFetch{RequestTimeoutMS: 3000, StorageTimeoutMS: 2000, NotBeforeMS: 5000},
		DeriveFetch: marketfetch.RouteFetchLimits("stockhk")}
}

// hkE2EGroup 按 Collector 由任务和标签成员拼分组的做法造一个港股分组：source_id 取行情源目录的登记，
// 传给数据源的代码由港股的标的规则换出（00700.XHKG → 00700）。
func hkE2EGroup(subjects ...string) marketfetch.TaskGroup {
	symbols := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		symbol, err := ResolveSymbol("eastmoney", "stockhk", "equity", subject)
		if err != nil {
			panic(err)
		}
		symbols[subject] = symbol
	}
	return marketfetch.TaskGroup{Provider: "eastmoney", SourceID: DefaultSourceID("stockhk", "eastmoney", "equity"), MarketType: "equity", MarketID: "stockhk", InstrumentType: "equity",
		DatasetID: hkE2EDataset, Frequency: "5m", Subjects: subjects, ExternalSymbols: symbols}
}

// hkCapturingTransport 不连网：记下发出去的请求地址，返回固定内容。
type hkCapturingTransport struct {
	mu   sync.Mutex
	urls []*url.URL
	body string
}

func (c *hkCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.urls = append(c.urls, req.URL)
	c.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(c.body)), Request: req}, nil
}

// 真实的请求地址：域名、路径取自编进二进制的 config/markets/stockhk/sources/eastmoney.yaml（与装配时交给行情源目录的相同），
// 这里只把 HTTP 传输换成记录请求的假传输。16:00 那一期取 00005：secid 是 116.00005，klt=5，beg、end 是当天的日期。
func TestStockHKRequestGoesToTheConfiguredEastmoneyAddress(t *testing.T) {
	route, found, err := marketfetch.LoadRoute("stockhk")
	require.NoError(t, err)
	require.True(t, found)
	runtimes, err := marketfetch.LoadSourceRuntimes(route)
	require.NoError(t, err)
	settings := sourceSettings(runtimes["eastmoney"])

	storage := newHKRowStore()
	now := hkTime("2026-11-12 16:00:05")
	clock := func() time.Time { return now }
	pipeline, err := NewKlinePipeline(storage, "stockhk", marketdata.InstrumentEquity, "5m", "", nil)
	require.NoError(t, err)
	transport := &hkCapturingTransport{body: `{"data":{"klines":["2026-11-12 16:00,141.100,141.100,141.200,141.000,88000,12417320.000"]}}`}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(hkeastmoney.New(hkeastmoney.Config{
		BaseURL: settings.BaseURL, KlineEndpoint: settings.KlineEndpoint, HTTPClient: &http.Client{Transport: transport}, Now: clock,
		RateLimit: settings.RateLimit, MaxBarsPerRequest: settings.MaxBarsPerRequest,
	})))
	router, err := marketdata.NewRouter(registry, pipeline.Router.BreakerThreshold(), nil, nil)
	require.NoError(t, err)
	pipeline.Router, pipeline.Now = router, clock

	target := hkTime("2026-11-12 15:55:00")
	payload, err := pipeline.Execute(context.Background(), marketfetch.Request{
		BatchID: "address", BatchKind: domain.BatchKindRealtime, SpaceID: "stockhk", MarketID: "stockhk", InstrumentType: "equity",
		DatasetID: hkE2EDataset, Frequency: "5m", MarketType: "equity", RequestID: "address", Concurrency: 1,
		Items: []domain.CollectionItem{{
			SubjectID: "00005.XHKG", Symbol: "00005", Provider: "eastmoney", MarketType: "equity", DataType: "kline", DatasetID: hkE2EDataset, Frequency: "5m",
			StartTime: target.Format(time.RFC3339Nano), EndTime: target.Add(5 * time.Minute).Format(time.RFC3339Nano), BarLimit: 1,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus(), payload.GetErrorSummary())
	require.Len(t, storage.rows, 1)

	require.Len(t, transport.urls, 1)
	got := transport.urls[0]
	require.Equal(t, "https", got.Scheme)
	require.Equal(t, "push2his.eastmoney.com", got.Host)
	require.Equal(t, "/api/qt/stock/kline/get", got.Path)
	query := got.Query()
	require.Equal(t, "116.00005", query.Get("secid"))
	require.Equal(t, "5", query.Get("klt"))
	require.Equal(t, "0", query.Get("fqt"))
	require.Equal(t, "20261112", query.Get("beg"))
	require.Equal(t, "20261112", query.Get("end"))
}
