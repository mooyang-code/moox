package fetchplan

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

type memWorkers struct{ worker *store.StreamWorker }

func (w *memWorkers) Get(context.Context, string) (*store.StreamWorker, error) { return w.worker, nil }

func (w *memWorkers) apply(id int64) {
	w.worker = &store.StreamWorker{SpaceID: "crypto", InstanceID: "i", AppliedPlanID: id, Status: "{}", HeartbeatAt: time.Now()}
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
			ResultDatasetID: "ds_spot_" + frequency, CollectParams: `{"frequency":"` + frequency + `"}`,
		})
	}
	h.r = h.newReconciler()
	return h
}

// setIngest 模拟切换命令：改写这个数据集的任务的采集方式。
func (h *ingestHarness) setIngest(datasetID, ingest string) {
	h.t.Helper()
	for i := range h.tasks.tasks {
		if h.tasks.tasks[i].ResultDatasetID == datasetID {
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

// settle 推进时间并让 collector-stream 跟上，直到没有未生效的计划、已生效状态里没有待裁的旧份与停止份、各数据集都已到生效点；
// 返回期间最后一份计划记录（没有新计划时为 nil）。
func (h *ingestHarness) settle(confirmStream bool) *store.FetchPlanRecord {
	h.t.Helper()
	var last *store.FetchPlanRecord
	for i := 0; i < 2000; i++ {
		require.NoError(h.t, h.reconcile(h.r))
		if open := h.open(); open != nil {
			progress, err := decodeFetchPlanProgress(open.Progress)
			require.NoError(h.t, err)
			if confirmStream && progress.StreamPushed {
				h.workers.apply(open.ID)
			}
		} else if h.converged() {
			if record := h.lastRecord(); last == nil || record.ID != last.ID {
				last = record
			}
			return last
		}
		last = h.lastRecord()
		h.now = h.now.Add(20 * time.Second)
	}
	h.t.Fatal("plan did not settle")
	return nil
}

func (h *ingestHarness) applied() *FetchPlanContent {
	return h.appliedContent()
}

const (
	ingestStream = marketfetch.PlanExecutorStreaming
	ingestSCF    = marketfetch.PlanExecutorSCF
	ingestLocal  = marketfetch.PlanExecutorLocal
	streamItem   = "stream-crypto-spot"
	localItem    = "local-crypto-spot"
)

// 新部署：默认 stream 的数据集直接进推送项，截止直接是 1m 5 秒、1h 8 秒，不经过交接。
func TestFreshDeploymentPutsDefaultStreamDatasetsOnTheStreamItem(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1m", "1h")
	h.settle(true)
	content := h.applied()
	require.Equal(t, marketfetch.PlanExecutorStreaming, content.Functions[streamItem].Executor)
	require.Len(t, content.Functions, 1, "没有 SCF 函数")
	require.Empty(t, h.cloudTimers())
	plan := content.Functions[streamItem].Target
	require.Equal(t, marketfetch.PlanExecutorStreaming, plan.Executor)
	require.Len(t, plan.Datasets[0].Copies[0].Subjects, 60)
	require.NoError(t, plan.Validate())
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"])
	require.Equal(t, 8, h.lateness.values["ds_spot_1h"])
}

// 交接：计划下发确认就生效；截止由之后每轮对账在“新份 From + 旧截止 + 余量”时改（幂等），改成功的那一轮裁掉旧份；
// 数据集都移走后，旧执行端的函数在最后一个旧份过窗口后停定时器。
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
	require.NotNil(t, record, "stream 还没确认")
	content := h.content(record)
	minute, hour := content.Datasets["ds_spot_1m|1m"], content.Datasets["ds_spot_1h|1h"]
	require.Equal(t, PlanActionAppend, minute.Action)
	require.False(t, minute.Register)
	require.GreaterOrEqual(t, minute.P, h.now.Add(planLeadTimeHandoff).Unix())
	require.Equal(t, minute.P, minute.HandoffFrom())
	require.Equal(t, DatasetCopy{From: minute.Copies[0].From, Hash: minute.Hash, Count: 60, LatenessS: 15, Ingest: ingestSCF}, minute.Copies[0])
	require.Equal(t, 5, minute.Copies[1].LatenessS)
	require.Equal(t, 5, minute.LatenessS)
	require.Equal(t, 8, hour.LatenessS)
	require.Len(t, h.registrar.registered["ds_spot_1m|1m"], registered, "交接不重新登记快照")
	require.Equal(t, 15, h.lateness.values["ds_spot_1m"], "截止不在建计划时改")
	// 旧执行端那一份按旧截止，新执行端那一份按新截止。
	stream := content.Functions[streamItem].Target
	require.Equal(t, 5, findPlanDataset(stream, "ds_spot_1m|1m").LatenessS)
	require.Equal(t, minute.P, findPlanDataset(stream, "ds_spot_1m|1m").Copies[0].From)
	for _, name := range scfNames {
		old := content.Functions[name].Target
		require.Equal(t, 15, findPlanDataset(old, "ds_spot_1m|1m").LatenessS, name)
		require.Empty(t, lastCopy(findPlanDataset(old, "ds_spot_1m|1m")).Subjects, "P 起旧执行端为空")
	}
	// stream 确认就生效，不等任何窗口。
	progress, _ := decodeFetchPlanProgress(record.Progress)
	require.True(t, progress.StreamPushed)
	require.False(t, progress.Pushed[streamItem])
	h.workers.apply(record.ID)
	require.NoError(t, h.reconcile(h.r))
	require.Nil(t, h.open(), "交接计划下发确认就生效")
	require.Equal(t, minute.P, h.applied().Datasets["ds_spot_1m|1m"].HandoffFrom(), "已生效状态里读得出交接生效点")

	// 1m 的截止在 P + 旧截止 + 余量改（对账在 P+10 秒那一轮里等到可改时刻），同一轮裁掉旧份。
	h.now = time.Unix(minute.P, 0).Add(10 * time.Second)
	h.r = h.newReconciler() // Collector 重启后续跑
	require.NoError(t, h.reconcile(h.r))
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"], "1m 在自己的窗口里改，不等 1h")
	require.False(t, h.now.Before(time.Unix(minute.P, 0).Add(15*time.Second+copyWindowMargin)), "不早于旧周期都已建立")
	require.True(t, h.now.Before(time.Unix(minute.P, 0).Add(time.Minute)), "在新执行端第一个周期建立之前")
	require.Equal(t, 15, h.lateness.values["ds_spot_1h"])
	require.Len(t, h.applied().Datasets["ds_spot_1m|1m"].Copies, 1, "截止改成功的那一轮裁掉 1m 旧份")
	for _, name := range scfNames {
		require.True(t, h.timerEnabled(name), "1h 旧份还在，%s 照常运行", name)
	}
	// 1h：可改时刻在本轮等待范围内，等到之后就改，随即裁掉旧份；旧执行端函数没有数据集了，停定时器。
	h.now = time.Unix(hour.P, 0).Add(15*time.Second + copyWindowMargin - time.Second)
	require.NoError(t, h.reconcile(h.r))
	require.Equal(t, 8, h.lateness.values["ds_spot_1h"])
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"], "截止取当前期望，不会改回旧值")
	require.Nil(t, h.open())
	for _, name := range scfNames {
		require.False(t, h.timerEnabled(name), "最后一个旧份过窗口后停用 %s 的定时器", name)
		require.Nil(t, h.applied().Functions[name].Target)
	}
	require.Len(t, h.applied().Functions[streamItem].Target.Datasets, 2)

	// 之后的计划不会把属性改回旧值。
	h.now = h.now.Add(time.Hour)
	require.NoError(t, h.reconcile(h.r))
	require.Nil(t, h.open())
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"])

	// 切回 scf：截止 5 → 15 同样不在建计划时改。
	h.setIngest("ds_spot_1m", ingestSCF)
	require.NoError(t, h.reconcile(h.r))
	content = h.content(h.lastRecord())
	require.Equal(t, ingestStream, content.Datasets["ds_spot_1m|1m"].Copies[0].Ingest)
	require.Equal(t, 5, content.Datasets["ds_spot_1m|1m"].Copies[0].LatenessS)
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"])
	require.Equal(t, 8, h.lateness.values["ds_spot_1h"], "未切换的数据集不动")
	h.settle(true)
	require.Equal(t, 15, h.lateness.values["ds_spot_1m"])
	applied := h.applied()
	require.Len(t, applied.Functions[streamItem].Target.Datasets, 1, "stream 只保留 1h")
	scfCount := 0
	for _, function := range applied.Functions {
		if function.IsSCF() && function.Target != nil {
			scfCount++
			require.Len(t, function.Target.Datasets, 1, "SCF 只拿采集方式是 scf 的数据集")
			require.Equal(t, "ds_spot_1m", function.Target.Datasets[0].DatasetID)
		}
	}
	require.Positive(t, scfCount)
}

// 交接截止调用失败时旧份不裁（按等待处理）；成功后裁掉旧份。期间别的数据集的变化照常建计划。
func TestHandoffKeepsTheOldCopyUntilTheLatenessCallSucceeds(t *testing.T) {
	h := newIngestHarness(t, ingestSCF, "1m", "1h")
	h.settle(true)
	h.setIngest("ds_spot_1m", ingestStream)
	require.NoError(t, h.reconcile(h.r))
	record := h.open()
	require.NotNil(t, record)
	p := h.content(record).Datasets["ds_spot_1m|1m"].P
	h.workers.apply(record.ID)
	require.NoError(t, h.reconcile(h.r))
	require.Nil(t, h.open())

	h.lateness.failFor = map[string]bool{"ds_spot_1m": true}
	h.now = time.Unix(p, 0).Add(30 * time.Second)
	require.Error(t, h.reconcile(h.r), "截止改不成功按失败上报")
	require.Len(t, h.applied().Datasets["ds_spot_1m|1m"].Copies, 2, "截止没改成功，旧份不裁")
	require.Equal(t, 15, h.lateness.values["ds_spot_1m"])

	// 期间 1h 的标的变了：照常建计划（1m 本轮等待，不随它变化）。
	h.setSubjects(61)
	h.now = h.now.Add(20 * time.Second)
	require.Error(t, h.reconcile(h.r))
	latest := h.content(h.lastRecord())
	require.Equal(t, PlanActionAppend, latest.Datasets["ds_spot_1h|1h"].Action, "别的数据集的变化照常建计划")
	require.Equal(t, PlanActionKeep, latest.Datasets["ds_spot_1m|1m"].Action)
	require.Len(t, latest.Datasets["ds_spot_1m|1m"].Copies, 2)
	require.Contains(t, h.r.Issue(), "set allowed lateness")

	h.lateness.failFor = nil
	h.now = h.now.Add(20 * time.Second)
	h.settle(true)
	require.Equal(t, 5, h.lateness.values["ds_spot_1m"])
	require.Len(t, h.applied().Datasets["ds_spot_1m|1m"].Copies, 1, "成功后裁掉旧份")
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
	require.Equal(t, ingestSCF, h.applied().Datasets["ds_spot_1m|1m"].Ingest)
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
	handoff := h.content(h.lastRecord())
	require.Equal(t, ingestStream, handoff.Datasets["ds_spot_1m|1m"].Copies[0].Ingest, "交接随即开始")
	require.Positive(t, handoff.Datasets["ds_spot_1m|1m"].HandoffFrom())
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
	// Collector 刚重启：新进程不满 180 秒，照样等确认。
	h.startedAt = h.now
	h.r = h.newReconciler()
	require.NoError(t, h.reconcile(h.r))
	record := h.open()
	require.NotNil(t, record)
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
	first := h.content(h.lastRecord())
	require.True(t, first.Datasets["ds_spot_1m|1m"].Register)
	require.Zero(t, first.Datasets["ds_spot_1m|1m"].HandoffFrom())
	require.Nil(t, first.Functions[streamItem], "这份计划不含交接")
	require.Nil(t, h.open())
	// 交接要再加一份，而旧份还在窗口里：等（W），不建计划。
	firstID := h.lastRecord().ID
	require.NoError(t, h.reconcile(h.r))
	require.Equal(t, firstID, h.lastRecord().ID)
	require.Contains(t, h.r.Issue(), "等旧份窗口结束")
	// 旧份过窗口那一轮：同一份计划里裁掉旧份并加上交接的一份。
	h.now = time.Unix(first.Datasets["ds_spot_1m|1m"].P, 0).Add(15*time.Second + copyWindowMargin)
	require.NoError(t, h.reconcile(h.r))
	second := h.content(h.lastRecord())
	require.NotEqual(t, firstID, h.lastRecord().ID)
	require.True(t, second.Datasets["ds_spot_1m|1m"].Trimmed)
	require.Equal(t, PlanActionAppend, second.Datasets["ds_spot_1m|1m"].Action)
	require.False(t, second.Datasets["ds_spot_1m|1m"].Register)
	require.Equal(t, ingestSCF, second.Datasets["ds_spot_1m|1m"].Copies[0].Ingest)
	require.Positive(t, second.Datasets["ds_spot_1m|1m"].HandoffFrom())
	h.settle(true)
	require.Len(t, h.applied().Functions[streamItem].Target.Datasets[0].Copies[0].Subjects, 62)
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
			require.Equal(t, "ABCUSDT_X", function.Plan.Pairs["ABC-USDT"], name)
		} else {
			require.NotContains(t, string(raw), "pairs", name)
			require.Equal(t, marketfetch.PlanExecutorSCF, function.Plan.Executor, name)
		}
	}
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
