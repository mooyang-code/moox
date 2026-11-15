//go:build cgo

package e2e

// View 就绪耗时压测：从写入方提交到收到 ViewDataReady 的耗时，三种场景。
//
// 默认跳过。开启方式（在 modules/storage 目录下）：
//
//	MOOX_VIEW_READY_BENCH=1 go test ./internal/service/e2e/ -run TestViewReadyLatencyBench -count=1 -v -timeout 60m
//
// 可调参数（环境变量，均为可选）：
//
//	MOOX_VIEW_READY_BENCH_ROUNDS        每个场景计入统计的轮数，默认 5（另有 1 轮预热不计入）
//	MOOX_VIEW_READY_BENCH_SUBJECTS      K 线与因子场景的标的数，默认 501
//	MOOX_VIEW_READY_BENCH_KLINE_BATCHES 一期 K 线拆成几次 CommitTimeSeriesBatch，默认 1（整市场一次提交）
//	MOOX_VIEW_READY_BENCH_FACTOR_COLS   因子结果的列数，默认 20（501 标的时每期写入约 300KB，与线上因子结果行事件的实测量级一致）
//	MOOX_VIEW_READY_BENCH_IMPORT_BARS   导入场景单标的写入的根数，默认 5000
//	MOOX_VIEW_READY_BENCH_PREFILL_BARS  每个序列预先直接写进 View 索引的历史根数，默认 10000（生产 View 每序列保留 1 万根）
//	MOOX_VIEW_READY_BENCH_IDLE_VIEWS    另建多少个没有写入的数据集与 View，默认 0
//
// 本文件只放场景、数据和统计；装配（View 跟随变更日志）在 readyBenchEnv 的实现 view_follow_env_test.go 里。

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	readyBenchSpace     = "bench"
	readyBenchFreq      = "5m"
	readyBenchStep      = 5 * time.Minute
	readyBenchKlineSet  = "bench_kline_5m"
	readyBenchFactorSet = "bench_factor_5m"
	// readyBenchWriteBatchRows 与因子写入（pipeline.Write 默认 1000）和 CLI 导入（importStorageBatchRows）一致。
	readyBenchWriteBatchRows = 1000
	// readyBenchLatenessSeconds 是数据集的 allowed_lateness_seconds。
	readyBenchLatenessSeconds = 60
)

// readyBenchBase 是第一期 K 线的开始时间；预填历史都在它之前，导入场景的区间也在它之前。
var readyBenchBase = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

// readyBenchKlineColumns 与 Collector 加密货币 K 线一致（marketfetch/kline_committer.go）。
var readyBenchKlineColumns = []readyBenchColumn{
	{"open", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"high", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	{"low", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	{"volume", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {"quote_volume", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	{"trade_num", pb.FieldValueType_FIELD_VALUE_TYPE_INT},
}

type readyBenchColumn struct {
	name string
	kind pb.FieldValueType
}

type readyBenchParams struct {
	rounds       int
	subjects     int
	klineBatches int
	factorCols   int
	importBars   int
	prefillBars  int
	idleViews    int
}

func readyBenchParamsFromEnv(t *testing.T) readyBenchParams {
	t.Helper()
	read := func(name string, fallback, min int) int {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return fallback
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < min {
			t.Fatalf("%s=%q 不合法：要求不小于 %d 的整数", name, raw, min)
		}
		return value
	}
	return readyBenchParams{
		rounds:       read("MOOX_VIEW_READY_BENCH_ROUNDS", 5, 1),
		subjects:     read("MOOX_VIEW_READY_BENCH_SUBJECTS", 501, 1),
		klineBatches: read("MOOX_VIEW_READY_BENCH_KLINE_BATCHES", 1, 1),
		factorCols:   read("MOOX_VIEW_READY_BENCH_FACTOR_COLS", 20, 1),
		importBars:   read("MOOX_VIEW_READY_BENCH_IMPORT_BARS", 5000, 1),
		prefillBars:  read("MOOX_VIEW_READY_BENCH_PREFILL_BARS", 10000, 0),
		idleViews:    read("MOOX_VIEW_READY_BENCH_IDLE_VIEWS", 0, 0),
	}
}

// readyBenchDataset 是压测用的一个时序数据集与它唯一的 View（列与数据集相同）。
type readyBenchDataset struct {
	datasetID string
	viewID    string
	columns   []readyBenchColumn
	subjects  []string
	// prefillBars 是每个标的在 readyBenchBase 之前预先直接写进 View 索引的根数，让索引规模接近生产。
	prefillBars int
}

// readyBenchPrimary 是写入方使用的 PrimaryStore 写接口；新旧路径都不变。
type readyBenchPrimary interface {
	RegisterSeriesSnapshot(context.Context, *pb.PrimaryRegisterSeriesSnapshotReq) (*pb.PrimaryRegisterSeriesSnapshotRsp, error)
	CommitTimeSeriesBatch(context.Context, *pb.PrimaryCommitTimeSeriesBatchReq) (*pb.PrimaryCommitTimeSeriesBatchRsp, error)
	UpsertFields(context.Context, *pb.PrimaryUpsertFieldsReq) (*pb.PrimaryUpsertFieldsRsp, error)
	ReportDatasetPeriodCompleted(context.Context, *pb.ReportDatasetPeriodCompletedReq) (*pb.ReportDatasetPeriodCompletedRsp, error)
}

// readyBenchEnv 是一套装配好的 Storage（Primary + View + EventBus）。实现负责登记数据集、建好并预填 View、
// 启动应用路径；ViewDataReady 经 EventBus 发布，由 readyWatcher 在订阅端接收计时。
type readyBenchEnv interface {
	Primary() readyBenchPrimary
	Auth() *pb.AuthInfo
	// SetPeriodClock 设置周期账本使用的时钟（登记快照、建立周期、判定截止都按它）。
	SetPeriodClock(time.Time)
	// Watcher 返回 ViewDataReady 的订阅端。
	Watcher() *readyWatcher
	// CountViewRows 返回 View 在 [start, end) 内的行数（subject 为空时不限标的），用来确认就绪时行已全部可查。
	CountViewRows(ctx context.Context, viewID, subject string, start, end time.Time) (uint64, error)
}

// readyBenchSample 是一轮的计时：total 从第一次写入调用开始到收到 ViewDataReady；write 是写入调用本身的耗时；
// fromMarker 是从完成标记写入（账本场景即最后一次提交）返回到收到就绪的耗时。
type readyBenchSample struct {
	total, write, fromMarker time.Duration
}

func TestViewReadyLatencyBench(t *testing.T) {
	if os.Getenv("MOOX_VIEW_READY_BENCH") != "1" {
		t.Skip("View 就绪耗时压测默认跳过；设置 MOOX_VIEW_READY_BENCH=1 开启")
	}
	params := readyBenchParamsFromEnv(t)
	// 开发机读不到主机内存时 View 的 DuckDB 只给 256MB；按生产存储机（15.5GB 的 1/16）设置，可用同名环境变量覆盖。
	if os.Getenv("MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT") == "" {
		t.Setenv("MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT", "967MB")
	}
	subjects := make([]string, params.subjects)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("S%04d-USDT", i)
	}
	factorColumns := make([]readyBenchColumn, params.factorCols)
	for i := range factorColumns {
		factorColumns[i] = readyBenchColumn{name: fmt.Sprintf("factor_%03d", i), kind: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}
	}
	kline := readyBenchDataset{datasetID: readyBenchKlineSet, viewID: readyBenchKlineSet + "_view", columns: readyBenchKlineColumns, subjects: subjects, prefillBars: params.prefillBars}
	factor := readyBenchDataset{datasetID: readyBenchFactorSet, viewID: readyBenchFactorSet + "_view", columns: factorColumns, subjects: subjects, prefillBars: params.prefillBars}
	t.Logf("压测参数：轮数=%d（另有 1 轮预热） 标的=%d K线提交次数=%d 因子列=%d 导入根数=%d 预填根数/序列=%d",
		params.rounds, params.subjects, params.klineBatches, params.factorCols, params.importBars, params.prefillBars)

	ctx := context.Background()
	setupStart := time.Now()
	// 另建 idleViews 个没有写入的数据集与 View，它们的跟随器一直在服务端等待新变更，看几十个空闲跟随器的开销。
	idle := make([]readyBenchDataset, params.idleViews)
	for i := range idle {
		idle[i] = readyBenchDataset{datasetID: fmt.Sprintf("bench_idle_%02d", i), viewID: fmt.Sprintf("bench_idle_%02d_view", i), columns: readyBenchKlineColumns[:1]}
	}
	var env readyBenchEnv = newFollowEnv(t, append([]readyBenchDataset{kline, factor}, idle...), followEnvOptions{prebuilt: true})
	t.Logf("装配与预填耗时 %s（空闲 View %d 个）", time.Since(setupStart).Round(time.Millisecond), len(idle))

	results := []struct {
		name    string
		samples []readyBenchSample
	}{
		{"K线全市场一期（CommitTimeSeriesBatch，账本判定完成）", runKlinePeriodBench(t, ctx, env, kline, params)},
		{"因子结果宽行（UpsertFields + ReportDatasetPeriodCompleted）", runFactorPeriodBench(t, ctx, env, factor, params)},
		{fmt.Sprintf("导入单标的 %d 根（UpsertFields 每批 %d 行 + 完成标记）", params.importBars, readyBenchWriteBatchRows), runImportBench(t, ctx, env, kline, params)},
	}
	for _, result := range results {
		t.Logf("结果 %s：%s", result.name, summarizeReadySamples(result.samples))
	}
}

// runKlinePeriodBench：每轮一个新周期，全市场一期的行按 klineBatches 拆分提交；最后一次提交让账本判定完成，
// Storage 生成 ledger 完成标记，View 应用行与标记后发布 ViewDataReady。
func runKlinePeriodBench(t *testing.T, ctx context.Context, env readyBenchEnv, dataset readyBenchDataset, params readyBenchParams) []readyBenchSample {
	t.Helper()
	env.SetPeriodClock(readyBenchBase.Add(-time.Minute))
	series := make([]*pb.SeriesSnapshotEntry, len(dataset.subjects))
	for i, subject := range dataset.subjects {
		series[i] = &pb.SeriesSnapshotEntry{SubjectId: subject}
	}
	snapshot, err := env.Primary().RegisterSeriesSnapshot(ctx, &pb.PrimaryRegisterSeriesSnapshotReq{
		AuthInfo: env.Auth(), SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, Frequency: readyBenchFreq,
		Series: series, EffectiveFrom: readyBenchBase.Unix(),
	})
	if err != nil || snapshot.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("登记序列快照：rsp=%v err=%v", snapshot, err)
	}
	samples := make([]readyBenchSample, 0, params.rounds)
	for round := 0; round <= params.rounds; round++ {
		period := readyBenchBase.Add(time.Duration(round) * readyBenchStep)
		// 周期刚收线：截止 = 收线 + allowed_lateness，时钟放在两者之间。
		env.SetPeriodClock(period.Add(readyBenchStep + time.Second))
		items := make([]*pb.TimeSeriesBatchRow, len(dataset.subjects))
		for i, subject := range dataset.subjects {
			items[i] = &pb.TimeSeriesBatchRow{SeriesIndex: uint32(i), Row: readyBenchRow(dataset, subject, period, round)}
		}
		waiter := env.Watcher().Expect(dataset.viewID, period.Unix())
		start := time.Now()
		per := (len(items) + params.klineBatches - 1) / params.klineBatches
		for from := 0; from < len(items); from += per {
			to := min(from+per, len(items))
			rsp, err := env.Primary().CommitTimeSeriesBatch(ctx, &pb.PrimaryCommitTimeSeriesBatchReq{
				AuthInfo: env.Auth(), Items: items[from:to],
				Expectation: &pb.DatasetPeriodExpectation{
					SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, Frequency: readyBenchFreq, PeriodTime: period.Unix(),
					SeriesHash: snapshot.GetSeriesHash(), ExpectedCount: snapshot.GetExpectedCount(),
				},
			})
			if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
				t.Fatalf("第 %d 轮提交 K 线：rsp=%v err=%v", round, rsp.GetRetInfo(), err)
			}
		}
		written := time.Now()
		readyAt := waiter.Wait(t, ctx)
		requireViewRows(t, ctx, env, dataset.viewID, period, period.Add(readyBenchStep), uint64(len(dataset.subjects)))
		sample := readyBenchSample{total: readyAt.Sub(start), write: written.Sub(start), fromMarker: readyAt.Sub(written)}
		logReadyRound(t, "K线", round, sample)
		if round > 0 {
			samples = append(samples, sample)
		}
	}
	return samples
}

// runFactorPeriodBench：因子引擎的写法，每期一次 UpsertFields（每批最多 1000 行），
// 写完再上报 producer 完成标记，View 直接发布 ViewDataReady。
func runFactorPeriodBench(t *testing.T, ctx context.Context, env readyBenchEnv, dataset readyBenchDataset, params readyBenchParams) []readyBenchSample {
	t.Helper()
	samples := make([]readyBenchSample, 0, params.rounds)
	for round := 0; round <= params.rounds; round++ {
		period := readyBenchBase.Add(time.Duration(round) * readyBenchStep)
		rows := make([]*pb.RowFieldUpsert, len(dataset.subjects))
		for i, subject := range dataset.subjects {
			rows[i] = readyBenchRow(dataset, subject, period, round)
		}
		if round == 0 {
			t.Logf("因子场景每期写入请求体约 %d 字节（%d 行 × %d 列）", proto.Size(&pb.PrimaryUpsertFieldsReq{Rows: rows}), len(rows), len(dataset.columns))
		}
		waiter := env.Watcher().Expect(dataset.viewID, period.Unix())
		start := time.Now()
		writeRowsInBatches(t, ctx, env, dataset, rows)
		markerAt := time.Now()
		reportProducerCompleted(t, ctx, env, dataset, period, dataset.subjects, fmt.Sprintf("bench-factor-cause-%d", period.Unix()))
		readyAt := waiter.Wait(t, ctx)
		requireViewRows(t, ctx, env, dataset.viewID, period, period.Add(readyBenchStep), uint64(len(dataset.subjects)))
		sample := readyBenchSample{total: readyAt.Sub(start), write: markerAt.Sub(start), fromMarker: readyAt.Sub(markerAt)}
		logReadyRound(t, "因子", round, sample)
		if round > 0 {
			samples = append(samples, sample)
		}
	}
	return samples
}

// runImportBench：导入或补算一个标的的长区间。写进 K 线数据集（索引已有全市场历史），每轮一个新标的，
// 区间在第一期之前、与预填不重叠，按 CLI 导入的批大小用 UpsertFields 写入，最后追加一条 producer 完成标记，
// 以它的 ViewDataReady 作为“全部行已应用”的终点（数据集内按顺序应用，标记在全部行之后）。
// 注：CLI 导入等的是 WaitViewApplied；这里统一用就绪事件作终点，与 T0 基线口径一致。
func runImportBench(t *testing.T, ctx context.Context, env readyBenchEnv, kline readyBenchDataset, params readyBenchParams) []readyBenchSample {
	t.Helper()
	samples := make([]readyBenchSample, 0, params.rounds)
	from := readyBenchBase.Add(-time.Duration(params.importBars) * readyBenchStep)
	to := readyBenchBase
	for round := 0; round <= params.rounds; round++ {
		subject := fmt.Sprintf("IMPORT%02d-USDT", round)
		dataset := kline
		dataset.subjects = []string{subject}
		rows := make([]*pb.RowFieldUpsert, 0, params.importBars)
		for at := from; at.Before(to); at = at.Add(readyBenchStep) {
			rows = append(rows, readyBenchRow(dataset, subject, at, round))
		}
		last := to.Add(-readyBenchStep)
		waiter := env.Watcher().Expect(dataset.viewID, last.Unix())
		start := time.Now()
		writeRowsInBatches(t, ctx, env, dataset, rows)
		markerAt := time.Now()
		reportProducerCompleted(t, ctx, env, dataset, last, []string{subject}, fmt.Sprintf("bench-import-%s", subject))
		readyAt := waiter.Wait(t, ctx)
		sample := readyBenchSample{total: readyAt.Sub(start), write: markerAt.Sub(start), fromMarker: readyAt.Sub(markerAt)}
		count, err := env.CountViewRows(ctx, dataset.viewID, subject, from, to)
		if err != nil || count != uint64(len(rows)) {
			t.Fatalf("第 %d 轮导入就绪后 View 里 %s 的行数=%d err=%v，应为 %d", round, subject, count, err, len(rows))
		}
		logReadyRound(t, "导入", round, sample)
		if round > 0 {
			samples = append(samples, sample)
		}
	}
	return samples
}

func writeRowsInBatches(t *testing.T, ctx context.Context, env readyBenchEnv, dataset readyBenchDataset, rows []*pb.RowFieldUpsert) {
	t.Helper()
	for from := 0; from < len(rows); from += readyBenchWriteBatchRows {
		to := min(from+readyBenchWriteBatchRows, len(rows))
		rsp, err := env.Primary().UpsertFields(ctx, &pb.PrimaryUpsertFieldsReq{
			AuthInfo: env.Auth(), SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, Rows: rows[from:to],
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("写入 %s：rsp=%v err=%v", dataset.datasetID, rsp.GetRetInfo(), err)
		}
	}
}

func reportProducerCompleted(t *testing.T, ctx context.Context, env readyBenchEnv, dataset readyBenchDataset, period time.Time, universe []string, cause string) {
	t.Helper()
	rsp, err := env.Primary().ReportDatasetPeriodCompleted(ctx, &pb.ReportDatasetPeriodCompletedReq{
		AuthInfo: env.Auth(), SpaceId: readyBenchSpace,
		Marker: &pb.DatasetPeriodCompletedMarker{
			DatasetId: dataset.datasetID, Frequency: readyBenchFreq, PeriodTime: period.Unix(), Status: "complete",
			UniverseSubjectIds: universe, CauseEventId: cause, CompletedAt: timestamppb.Now(),
			DetailType: "moox.bench.PeriodDetail.v1", Detail: []byte("bench"),
		},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("上报 %s 第 %d 期完成：rsp=%v err=%v", dataset.datasetID, period.Unix(), rsp.GetRetInfo(), err)
	}
}

func requireViewRows(t *testing.T, ctx context.Context, env readyBenchEnv, viewID string, start, end time.Time, want uint64) {
	t.Helper()
	got, err := env.CountViewRows(ctx, viewID, "", start, end)
	if err != nil || got != want {
		t.Fatalf("就绪后 View %s 在 [%s, %s) 的行数=%d err=%v，应为 %d", viewID, start.Format(time.RFC3339), end.Format(time.RFC3339), got, err, want)
	}
}

// readyBenchRow 生成一行：每列一个确定的值，round 让不同轮的值不同。
func readyBenchRow(dataset readyBenchDataset, subject string, at time.Time, round int) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key: &pb.RowKey{SpaceId: readyBenchSpace, DatasetId: dataset.datasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: subject, Freq: readyBenchFreq, DataTime: at.UTC().Format(time.RFC3339Nano),
		}}},
		Fields: readyBenchFields(dataset.columns, at, round),
	}
}

func readyBenchFields(columns []readyBenchColumn, at time.Time, round int) []*pb.FieldValue {
	fields := make([]*pb.FieldValue, len(columns))
	base := float64(at.Unix()%100000) + float64(round)
	for i, column := range columns {
		value := &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: base + float64(i)*0.25}}
		if column.kind == pb.FieldValueType_FIELD_VALUE_TYPE_INT {
			value = &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: int64(base) + int64(i)}}
		}
		fields[i] = &pb.FieldValue{FieldId: column.name, Value: value}
	}
	return fields
}

func logReadyRound(t *testing.T, scenario string, round int, sample readyBenchSample) {
	t.Helper()
	label := fmt.Sprintf("第 %d 轮", round)
	if round == 0 {
		label = "预热"
	}
	t.Logf("%s %s：提交到就绪=%s 写入调用=%s 标记写入后到就绪=%s", scenario, label,
		sample.total.Round(time.Millisecond), sample.write.Round(time.Millisecond), sample.fromMarker.Round(time.Millisecond))
}

func summarizeReadySamples(samples []readyBenchSample) string {
	pick := func(get func(readyBenchSample) time.Duration) (median, max time.Duration, all []string) {
		values := make([]time.Duration, len(samples))
		for i, sample := range samples {
			values[i] = get(sample)
			all = append(all, values[i].Round(time.Millisecond).String())
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		if len(values)%2 == 1 {
			median = values[len(values)/2]
		} else {
			median = (values[len(values)/2-1] + values[len(values)/2]) / 2
		}
		return median, values[len(values)-1], all
	}
	totalMedian, totalMax, totals := pick(func(s readyBenchSample) time.Duration { return s.total })
	writeMedian, writeMax, _ := pick(func(s readyBenchSample) time.Duration { return s.write })
	markerMedian, markerMax, _ := pick(func(s readyBenchSample) time.Duration { return s.fromMarker })
	return fmt.Sprintf("提交到就绪 中位数=%s 最大=%s（各轮 %s）；写入调用 中位数=%s 最大=%s；标记写入后到就绪 中位数=%s 最大=%s",
		totalMedian.Round(time.Millisecond), totalMax.Round(time.Millisecond), strings.Join(totals, " "),
		writeMedian.Round(time.Millisecond), writeMax.Round(time.Millisecond),
		markerMedian.Round(time.Millisecond), markerMax.Round(time.Millisecond))
}

// readyWatcher 订阅 ViewDataReady（新旧路径都由 View 发布到 EventBus），以订阅端收到消息的时刻为终点。
type readyWatcher struct {
	registry *events.Registry
	sub      *nats.Subscription
	mu       sync.Mutex
	waiters  map[string]chan time.Time
	errs     chan error
}

func newReadyWatcher(t *testing.T, nc *nats.Conn) *readyWatcher {
	t.Helper()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	w := &readyWatcher{registry: registry, waiters: make(map[string]chan time.Time), errs: make(chan error, 16)}
	family, err := registry.FamilyPattern(events.ViewDataReady)
	if err != nil {
		t.Fatal(err)
	}
	w.sub, err = nc.Subscribe(family, w.handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.sub.SetPendingLimits(-1, -1); err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.sub.Unsubscribe() })
	return w
}

func (w *readyWatcher) handle(msg *nats.Msg) {
	receivedAt := time.Now()
	_, ready, err := events.DecodeViewDataReadyWithContentType(w.registry, msg.Data, msg.Subject, msg.Header.Get(nats.MsgIdHdr), msg.Header.Get("Content-Type"))
	if err != nil {
		select {
		case w.errs <- fmt.Errorf("解码 ViewDataReady（%s）：%w", msg.Subject, err):
		default:
		}
		return
	}
	w.mu.Lock()
	ch := w.waiters[readyWaiterKey(ready.GetViewId(), ready.GetPeriodTime())]
	w.mu.Unlock()
	if ch != nil {
		select {
		case ch <- receivedAt:
		default: // 重复的就绪事件只取第一次
		}
	}
}

func readyWaiterKey(viewID string, periodTime int64) string {
	return viewID + "/" + strconv.FormatInt(periodTime, 10)
}

// Expect 在写入之前登记等待，避免就绪事件早于登记到达。
func (w *readyWatcher) Expect(viewID string, periodTime int64) *readyWaiter {
	ch := make(chan time.Time, 1)
	w.mu.Lock()
	w.waiters[readyWaiterKey(viewID, periodTime)] = ch
	w.mu.Unlock()
	return &readyWaiter{watcher: w, viewID: viewID, periodTime: periodTime, ch: ch}
}

type readyWaiter struct {
	watcher    *readyWatcher
	viewID     string
	periodTime int64
	ch         chan time.Time
}

func (r *readyWaiter) Wait(t *testing.T, ctx context.Context) time.Time {
	t.Helper()
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	select {
	case at := <-r.ch:
		return at
	case err := <-r.watcher.errs:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
		t.Fatalf("2 分钟内没有收到 View %s 第 %d 期的 ViewDataReady", r.viewID, r.periodTime)
	}
	return time.Time{}
}
