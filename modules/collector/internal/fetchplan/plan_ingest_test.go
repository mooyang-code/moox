package fetchplan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

type memWorkers struct{ worker *store.StreamWorker }

func (w *memWorkers) Get(context.Context, string) (*store.StreamWorker, error) { return w.worker, nil }

func (w *memWorkers) apply(id int64, stage string) {
	w.worker = &store.StreamWorker{SpaceID: "crypto", InstanceID: "i", Applied: mustJSON(StreamApplied{ID: id, Stage: stage}), Status: "{}", HeartbeatAt: time.Now()}
}

type ingestHarness struct {
	*planHarness
	workers *memWorkers
	// offline 为真表示 collector-stream 心跳超时。
	offline bool
	// startedAt 是 Collector 进程的启动时刻，默认早于测试起点一小时（早已启动）。
	startedAt time.Time
	r         *PlanReconciler
}

// newIngestHarness 建一个现货空间：每个频率一个 K 线任务，采集方式都是 ingest。
func newIngestHarness(t *testing.T, ingest string, frequencies ...string) *ingestHarness {
	h := &ingestHarness{planHarness: newPlanHarness(t, 60), workers: &memWorkers{}}
	h.startedAt = h.now.Add(-time.Hour)
	h.tasks.tasks = nil
	for _, frequency := range frequencies {
		h.tasks.tasks = append(h.tasks.tasks, domain.CollectionTask{
			SpaceID: "crypto", TaskID: "spot-" + frequency, Enabled: true, DataType: "kline", Ingest: ingest, TagIDs: []string{"binance_spot"},
			CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"ds_spot_` + frequency + `","frequency":"` + frequency + `"}`,
		})
	}
	h.r = h.newReconciler()
	return h
}

// setIngest 模拟切换命令：改写这个数据集的任务的采集方式。
func (h *ingestHarness) setIngest(datasetID, ingest string) {
	h.t.Helper()
	for i := range h.tasks.tasks {
		if strings.Contains(h.tasks.tasks[i].CollectParams, `"target_dataset_id":"`+datasetID+`"`) {
			h.tasks.tasks[i].Ingest = ingest
			return
		}
	}
	h.t.Fatalf("no task writes dataset %s", datasetID)
}

// newReconciler 模拟 Collector 重启：新的对账器实例从库里的计划与进度续跑。
func (h *ingestHarness) newReconciler() *PlanReconciler {
	r := h.reconciler()
	r.Workers = h.workers
	r.StreamOnline = func(context.Context) (bool, error) { return !h.offline, nil }
	r.StartedAt, r.StreamTimeout = h.startedAt, 180*time.Second
	r.Options.StreamLatenessS = map[string]int{"1m": 5, "1h": 8}
	return r
}

// lose 让 collector-stream 失联：不再确认，心跳超时。
func (h *ingestHarness) lose() { h.workers.worker, h.offline = nil, true }

// settle 推进时间并让 collector-stream 跟上，直到没有未生效的计划；返回生效的计划记录。
func (h *ingestHarness) settle(confirmStream bool) *store.FetchPlanRecord {
	h.t.Helper()
	var last *store.FetchPlanRecord
	for i := 0; i < 400; i++ {
		require.NoError(h.t, h.reconcile(h.r))
		open := h.open()
		if open == nil {
			return last
		}
		last = open
		progress, err := decodeFetchPlanProgress(open.Progress)
		require.NoError(h.t, err)
		if confirmStream && progress.StreamStage != "" {
			h.workers.apply(open.ID, progress.StreamStage)
		}
		h.now = h.now.Add(20 * time.Second)
	}
	h.t.Fatal("plan did not settle")
	return nil
}

func (h *ingestHarness) applied() *FetchPlanContent {
	record, err := h.store.LatestApplied(context.Background(), "crypto")
	require.NoError(h.t, err)
	require.NotNil(h.t, record)
	return h.content(record)
}

const (
	ingestStream = marketfetch.PlanExecutorStreaming
	ingestSCF    = marketfetch.PlanExecutorSCF
	ingestLocal  = marketfetch.PlanExecutorLocal
	streamItem   = "stream-crypto-spot"
	localItem    = "local-crypto-spot"
)

// 新部署：默认 stream 的数据集直接进推送项，截止直接是 1m 5 秒、1h 8 秒，不经过 drain。
func TestFreshDeploymentPutsDefaultStreamDatasetsOnTheStreamItem(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m", "1h")
	h.settle(true)
	content := h.applied()
	require.Equal(t, marketfetch.PlanExecutorStreaming, content.Functions[streamItem].Executor)
	require.Len(t, content.Functions, 1, "没有 SCF 函数")
	require.Empty(t, h.cloudTimers())
	plan := content.FinalPlan(content.Functions[streamItem])
	require.Equal(t, marketfetch.PlanExecutorStreaming, plan.Executor)
	require.Len(t, plan.Datasets[0].Copies[0].Subjects, 60)
	require.NoError(t, plan.Validate())
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"])
	require.Equal(t, 8, h.lateness.values["ds_spot_1h"])
}

func TestHandoffPullToStreamAndBack(t *testing.T) {
	h := newIngestHarness(t, ingestSCF, "1m", "1h")
	h.settle(true)
	require.Equal(t, 15, h.lateness.values["ds_spot_1m"])
	scfNames := h.applied().SortedFunctionNames()

	// 切到 stream：P 至少 3 分钟之后，hash 不变不重新登记，截止先不改。
	h.setIngest("ds_spot_1m", ingestStream)
	h.setIngest("ds_spot_1h", ingestStream)
	registered := len(h.registrar.registered["ds_spot_1m|1m"])
	require.NoError(t, h.reconcile(h.r))
	record := h.open()
	require.NotNil(t, record)
	content := h.content(record)
	minute, hour := content.Datasets["ds_spot_1m|1m"], content.Datasets["ds_spot_1h|1h"]
	require.True(t, minute.Changed)
	require.False(t, minute.HashChanged)
	require.GreaterOrEqual(t, minute.P, h.now.Add(planLeadTimeHandoff).Unix())
	require.Equal(t, marketfetch.PlanExecutorSCF, minute.OldIngest)
	require.Equal(t, 15, minute.OldLatenessS)
	require.Equal(t, 5, minute.LatenessS)
	require.Equal(t, 8, hour.LatenessS)
	require.Len(t, h.registrar.registered["ds_spot_1m|1m"], registered, "交接不重新登记快照")
	require.Equal(t, 15, h.lateness.values["ds_spot_1m"], "截止在 drain 才改")
	// 旧执行端那一份按旧截止，新执行端那一份按新截止。
	stream := content.TransitionalPlan(content.Functions[streamItem])
	require.Equal(t, 5, findPlanDataset(stream, "ds_spot_1m|1m").LatenessS)
	require.Equal(t, minute.P, findPlanDataset(stream, "ds_spot_1m|1m").Copies[0].From)
	for _, name := range scfNames {
		old := content.TransitionalPlan(content.Functions[name])
		require.Equal(t, 15, findPlanDataset(old, "ds_spot_1m|1m").LatenessS, name)
		require.Empty(t, lastCopy(findPlanDataset(old, "ds_spot_1m|1m")).Subjects, "P 起旧执行端为空")
	}
	// stream 没确认前不算下发完成。
	progress, _ := decodeFetchPlanProgress(record.Progress)
	require.Equal(t, StreamStageTransition, progress.StreamStage)
	require.False(t, progress.Transitioned[streamItem])
	h.workers.apply(record.ID, StreamStageTransition)
	require.NoError(t, h.reconcile(h.r))
	progress, _ = decodeFetchPlanProgress(h.open().Progress)
	require.True(t, progress.Transitioned[streamItem])

	// 截止按数据集在各自窗口里改：1m 在 P + 旧截止 + 余量之后、P 收盘之前（对账在 P+10 秒那一轮里等到可改时刻）。
	h.now = time.Unix(minute.P, 0).Add(10 * time.Second)
	h.r = h.newReconciler() // Collector 重启后续跑
	require.NoError(t, h.reconcile(h.r))
	require.NotNil(t, h.open())
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"], "1m 在自己的窗口里改，不等 1h")
	require.False(t, h.now.Before(time.Unix(minute.P, 0).Add(15*time.Second+planFinalizeMargin)), "不早于旧周期都已建立")
	require.True(t, h.now.Before(time.Unix(minute.P, 0).Add(time.Minute)), "在新执行端第一个周期建立之前")
	require.Equal(t, 15, h.lateness.values["ds_spot_1h"])
	// 1h 的旧周期在 drain 结束前不停用：WindowEnd 按旧截止算到 1h 的 P。
	h.now = time.Unix(hour.P, 0).Add(15*time.Second + planFinalizeMargin - time.Second)
	require.NoError(t, h.reconcile(h.r))
	require.Equal(t, 8, h.lateness.values["ds_spot_1h"], "1h 的可改时刻在本轮等待范围内，等到之后就改")
	h.now = h.now.Add(2 * time.Second)
	require.NoError(t, h.reconcile(h.r))
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"])
	require.Equal(t, 8, h.lateness.values["ds_spot_1h"])
	for _, name := range scfNames {
		require.False(t, h.timerEnabled(name), "drain 结束后才停用 %s 的定时器", name)
	}
	// stream 的过渡计划只有从 P 起的一份，与最终计划相同，不需要再确认一次。
	require.Nil(t, h.open())
	applied := h.applied()
	require.Len(t, applied.Functions[streamItem].New.Datasets, 2)

	// 下一份普通计划不会把属性改回旧值。
	h.now = h.now.Add(time.Hour)
	require.NoError(t, h.reconcile(h.r))
	require.Nil(t, h.open())
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"])

	// 切回 scf：截止 5 → 15 同样在 drain 才改。
	h.setIngest("ds_spot_1m", ingestSCF)
	require.NoError(t, h.reconcile(h.r))
	content = h.content(h.open())
	require.Equal(t, marketfetch.PlanExecutorStreaming, content.Datasets["ds_spot_1m|1m"].OldIngest)
	require.Equal(t, 5, content.Datasets["ds_spot_1m|1m"].OldLatenessS)
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"])
	require.Equal(t, 8, h.lateness.values["ds_spot_1h"], "未切换的数据集不动")
	h.settle(true)
	require.Equal(t, 15, h.lateness.values["ds_spot_1m"])
	applied = h.applied()
	require.Len(t, applied.Functions[streamItem].New.Datasets, 1, "stream 只保留 1h")
	scfCount := 0
	for _, function := range applied.Functions {
		if function.IsSCF() && function.New != nil {
			scfCount++
			require.Len(t, function.New.Datasets, 1, "SCF 只拿采集方式是 scf 的数据集")
			require.Equal(t, "ds_spot_1m", function.New.Datasets[0].DatasetID)
		}
	}
	require.Positive(t, scfCount)
}

// local（本地执行端定时拉取）：不建 SCF 函数，分配写成 local 项。
func TestPullOnTheLocalExecutorUsesALocalItem(t *testing.T) {
	h := newIngestHarness(t, ingestLocal, "1m")
	h.settle(true)
	content := h.applied()
	require.Equal(t, marketfetch.PlanExecutorLocal, content.Functions[localItem].Executor)
	require.Equal(t, marketfetch.PlanExecutorLocal, content.Functions[localItem].New.Executor)
	require.Equal(t, 15, h.lateness.values["ds_spot_1m"])
	require.Empty(t, h.cloudTimers())
}

// 固定规则一：collector-stream 只是旧执行端且已离线，交接不等它确认，只等新执行端（SCF）就生效。
func TestHandoffDoesNotWaitForAnOfflineOldStreamWorker(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m")
	h.settle(true)
	h.lose()
	h.setIngest("ds_spot_1m", ingestSCF)
	h.settle(false)
	require.Nil(t, h.open())
	require.Equal(t, ingestSCF, h.applied().Datasets["ds_spot_1m|1m"].IngestOrSCF())
	require.NotEmpty(t, h.cloudTimers())
}

// 在线但迟迟不确认的 collector-stream 不算离线：计划一直等它。
func TestHandoffWaitsForAnOnlineStreamWorkerThatHasNotConfirmed(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m")
	h.settle(true)
	h.setIngest("ds_spot_1m", ingestSCF)
	require.NoError(t, h.reconcile(h.r))
	record := h.open()
	require.NotNil(t, record)
	for i := 0; i < 20; i++ {
		h.now = h.now.Add(time.Minute)
		require.NoError(t, h.reconcile(h.r))
	}
	require.Equal(t, record.ID, h.open().ID, "心跳没超时就一直等确认")
}

// 固定规则二：数据集留在 collector-stream 上只是标的变了，它离线时这份标的变更计划也不等它确认，
// 按原 hash 与 P 推进完成；之后的交接照常进行。
func TestSubjectChangeIsNotStuckOnAnOfflineStreamWorker(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m")
	h.settle(true)
	h.setSubjects(61)
	require.NoError(t, h.reconcile(h.r))
	stuck := h.open()
	require.NotNil(t, stuck)
	stuckP := h.content(stuck).Datasets["ds_spot_1m|1m"].P
	for i := 0; i < 5; i++ {
		h.now = h.now.Add(time.Minute)
		require.NoError(t, h.reconcile(h.r))
	}
	require.Equal(t, stuck.ID, h.open().ID, "在线但不确认就卡住")
	h.lose()
	require.NoError(t, h.reconcile(h.r))
	require.Nil(t, h.open(), "离线后已打开的计划推进完成")
	require.Equal(t, stuckP, h.applied().Datasets["ds_spot_1m|1m"].P)
	h.setIngest("ds_spot_1m", ingestSCF)
	require.NoError(t, h.reconcile(h.r))
	handoff := h.content(h.open())
	require.Equal(t, ingestStream, handoff.Datasets["ds_spot_1m|1m"].OldIngest, "交接随即开始")
	h.settle(false)
	require.Nil(t, h.open())
}

// 固定规则三：有数据集正切到 collector-stream 上（它是新执行端）时，它离线也要等它确认，不会在它没应用计划时提前生效。
func TestHandoffToStreamStillWaitsForTheOfflineNewWorker(t *testing.T) {
	h := newIngestHarness(t, ingestSCF, "1m")
	h.settle(true)
	h.setIngest("ds_spot_1m", ingestStream)
	require.NoError(t, h.reconcile(h.r))
	record := h.open()
	require.NotNil(t, record, "建计划时它在线")
	h.lose()
	for i := 0; i < 20; i++ {
		h.now = h.now.Add(20 * time.Second)
		require.NoError(t, h.reconcile(h.r))
	}
	require.Equal(t, record.ID, h.open().ID, "新执行端没确认就不生效")
	h.offline = false
	h.settle(true)
	require.Nil(t, h.open())
	require.Equal(t, ingestStream, h.applied().Functions[streamItem].Executor)
}

// 固定规则四：Collector 进程启动不满一个心跳超时（180 秒）时不按离线放行，重启后心跳表里的时间是旧的；满了以后才放行。
func TestOfflineReleaseWaitsUntilTheCollectorHasBeenUpForOneHeartbeatTimeout(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m")
	h.settle(true)
	h.lose()
	h.setIngest("ds_spot_1m", ingestSCF)
	require.NoError(t, h.reconcile(h.r))
	record := h.open()
	require.NotNil(t, record)
	p := time.Unix(h.content(record).Datasets["ds_spot_1m|1m"].P, 0)
	// Collector 在旧窗口结束之后才重启：新进程不满 180 秒，照样等确认。
	h.now = p.Add(time.Minute)
	h.startedAt = h.now
	h.r = h.newReconciler()
	for i := 0; i < 8; i++ {
		require.NoError(t, h.reconcile(h.r))
		require.Equal(t, record.ID, h.open().ID, "启动 %s 时不按离线放行", h.now.Sub(h.startedAt))
		h.now = h.now.Add(20 * time.Second)
	}
	h.now = h.startedAt.Add(181 * time.Second)
	h.settle(false)
	require.Nil(t, h.open(), "启动满 180 秒后按离线放行")
}

// 交接与标的变更同时出现：先按已生效的执行端完成标的变更，下一份计划再交接。
func TestHandoffWaitsForAPendingSubjectChange(t *testing.T) {
	h := newIngestHarness(t, ingestSCF, "1m")
	h.settle(true)
	h.setSubjects(62)
	h.setIngest("ds_spot_1m", ingestStream)
	require.NoError(t, h.reconcile(h.r))
	first := h.content(h.open())
	require.True(t, first.Datasets["ds_spot_1m|1m"].HashChanged)
	require.Empty(t, first.Datasets["ds_spot_1m|1m"].OldIngest)
	require.Nil(t, first.Functions[streamItem], "这份计划不含交接")
	h.settle(true)
	require.NoError(t, h.reconcile(h.r))
	second := h.content(h.open())
	require.False(t, second.Datasets["ds_spot_1m|1m"].HashChanged)
	require.Equal(t, marketfetch.PlanExecutorSCF, second.Datasets["ds_spot_1m|1m"].OldIngest)
	h.settle(true)
	require.Len(t, h.applied().Functions[streamItem].New.Datasets[0].Copies[0].Subjects, 62)
}

// 推送计划里交易对与 symbol 不同的标的带 pairs；SCF 函数的计划不带。
func TestStreamPlanCarriesPairsOnly(t *testing.T) {
	state, err := BuildDesiredState(PlanOptions{SpaceID: "crypto", Markets: map[string][]string{"swap": {"ap-hongkong"}}, Fetch: marketfetch.PlanFetch{Concurrency: 1}, DefaultLatenessS: 15}, []TaskGroup{
		{Provider: "binance", SourceID: "swap_http", MarketType: "swap", MarketID: "crypto", InstrumentType: "swap", DatasetID: "ds_stream", Frequency: "1m", Ingest: ingestStream, Subjects: []string{"ABC-USDT"}, Pairs: map[string]string{"ABC-USDT": "ABCUSDT_X"}},
		{Provider: "binance", SourceID: "swap_http", MarketType: "swap", MarketID: "crypto", InstrumentType: "swap", DatasetID: "ds_pull", Frequency: "1m", Subjects: []string{"ABC-USDT"}, Pairs: map[string]string{"ABC-USDT": "ABCUSDT_X"}},
	}, nil)
	require.NoError(t, err)
	for name, function := range state.Functions {
		raw, _ := json.Marshal(function.Plan)
		if function.Executor == marketfetch.PlanExecutorStreaming {
			require.Equal(t, "ABCUSDT_X", function.Plan.Pair("ABC-USDT"), name)
		} else {
			require.NotContains(t, string(raw), "pairs", name)
			require.NotContains(t, string(raw), "executor", name)
		}
	}
}

// 截止的可改窗口：旧周期都已建立之后（P + 旧截止 + 余量）、新执行端第一个周期收盘之前（P 加一个周期）。
func TestHandoffLatenessWindow(t *testing.T) {
	p := time.Date(2026, 10, 21, 8, 37, 0, 0, time.UTC)
	due, deadline := handoffLatenessDue(&PlanDatasetChange{PlanDatasetState: PlanDatasetState{Frequency: "1m", LatenessS: 20}, P: p.Unix(), OldLatenessS: 5})
	require.Equal(t, p.Add(10*time.Second), due, "stream→SCF：5 秒截止的旧周期在 P+5 之后不能再建立")
	require.Equal(t, p.Add(time.Minute), deadline)
	due, deadline = handoffLatenessDue(&PlanDatasetChange{PlanDatasetState: PlanDatasetState{Frequency: "1h", LatenessS: 8}, P: p.Truncate(time.Hour).Add(time.Hour).Unix(), OldLatenessS: 20})
	require.Equal(t, time.Date(2026, 10, 21, 9, 0, 25, 0, time.UTC), due)
	require.Equal(t, time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC), deadline)
}

// 旧端（collector-stream）在线但始终不确认：截止仍按时间在窗口里改成新执行端的值，
// 新执行端（SCF）P 起的周期不会因为截止还是旧值而整期被拒。
func TestHandoffLatenessSwitchesOnTimeWhenTheOldSideNeverConfirms(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m")
	h.settle(true)
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"])
	h.workers.worker = nil
	h.setIngest("ds_spot_1m", ingestSCF)
	require.NoError(t, h.reconcile(h.r))
	record := h.open()
	require.NotNil(t, record)
	p := time.Unix(h.content(record).Datasets["ds_spot_1m|1m"].P, 0)
	h.now = p.Add(5 * time.Second)
	require.NoError(t, h.reconcile(h.r))
	require.Equal(t, 15, h.lateness.values["ds_spot_1m"], "P + 旧截止 + 余量（10 秒）时改成 SCF 的截止")
	require.True(t, h.now.Before(p.Add(time.Minute)), "在新执行端第一个周期建立之前")
	require.Equal(t, record.ID, h.open().ID, "旧端不确认，计划本身仍等着")
}

// 一个数据集只有一个写入方：同一数据集的任务组采集方式不同，期望状态算不出来。
func TestDesiredStateRejectsOneDatasetWithTwoIngests(t *testing.T) {
	group := TaskGroup{Provider: "binance", SourceID: "spot_http", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds", Frequency: "1m", Subjects: []string{"ABC-USDT"}, Ingest: ingestStream}
	other := group
	other.Ingest = ingestSCF
	_, err := BuildDesiredState(PlanOptions{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-hongkong"}, "swap": {"ap-hongkong"}}, Fetch: marketfetch.PlanFetch{Concurrency: 1}, DefaultLatenessS: 15},
		[]TaskGroup{group, other}, nil)
	require.ErrorContains(t, err, "conflicting ingest")
}

// 一个数据集只放一个市场的序列：现货、永续两组写同一个数据集时期望状态算不出来（现货与永续要拆成两个任务）。
func TestDesiredStateRejectsOneDatasetFedByTwoMarkets(t *testing.T) {
	group := TaskGroup{Provider: "binance", SourceID: "spot_http", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds", Frequency: "1m", Subjects: []string{"ABC-USDT"}, Ingest: ingestSCF}
	other := group
	other.SourceID, other.MarketType, other.InstrumentType = "swap_http", "swap", "perpetual"
	_, err := BuildDesiredState(PlanOptions{SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-hongkong"}, "swap": {"ap-hongkong"}}, Fetch: marketfetch.PlanFetch{Concurrency: 1}, DefaultLatenessS: 15},
		[]TaskGroup{group, other}, nil)
	require.ErrorContains(t, err, "fed by two markets")
}
