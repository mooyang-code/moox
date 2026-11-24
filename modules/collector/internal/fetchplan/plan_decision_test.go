package fetchplan

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

// planSim 不经对账器、只用 BuildPlanContent 推进已生效状态：建出的计划立即登记（假 Storage 按真实规则判断）、
// 写时间线（同一生效点先删后插，与 store 一致）、生效。用来逐行验证决策表与各种时间线、登记的不变式。
type planSim struct {
	t         *testing.T
	options   PlanOptions
	groups    []TaskGroup
	functions map[string]*PlanFunction
	datasets  map[string]*PlanDataset
	storage   *fakeRegistrar
	timeline  []store.SeriesTimelineEntry
	now       time.Time
	// written 记下每次写行的“数据集|频率”与生效点，检查单调性。
	written map[string][]int64
}

func newPlanSim(t *testing.T, now time.Time, groups []TaskGroup) *planSim {
	options := testPlanOptions()
	options.DefaultLatenessS = 60
	sim := &planSim{t: t, options: options, groups: groups, storage: &fakeRegistrar{}, now: now, written: map[string][]int64{}}
	sim.storage.clock = func() time.Time { return sim.now }
	return sim
}

// step 按当前 groups 与提前量跑一轮：返回本轮的计划内容（没有变化时也返回）、是否建了计划、等待原因。
func (s *planSim) step(lead time.Duration) (*FetchPlanContent, bool, []string) {
	s.t.Helper()
	desired, err := BuildDesiredState(s.options, s.groups, s.functions)
	require.NoError(s.t, err)
	content, changed, waiting, err := BuildPlanContent(s.functions, s.datasets, desired, PlanTiming{Now: s.now, Lead: lead, LatenessOK: noHandoffs{}})
	require.NoError(s.t, err)
	if !changed {
		return content, false, waiting
	}
	for _, function := range content.Functions {
		if function.Target != nil {
			require.NoError(s.t, function.Target.Validate(), function.Name)
		}
	}
	s.writeTimeline(content.planCreatedTimeline("crypto"))
	for _, key := range content.sortedDatasetKeys() {
		dataset := content.Datasets[key]
		if !dataset.Register {
			continue
		}
		registration, err := s.storage.RegisterSeriesSnapshot(context.Background(), "crypto", dataset.DatasetID, dataset.Frequency, dataset.Series, dataset.P)
		require.NoError(s.t, err, "%s 登记 %s 被拒", key, time.Unix(dataset.P, 0).UTC())
		require.Equal(s.t, dataset.Hash, registration.Hash)
		if dataset.Timeline {
			s.writeTimeline([]store.SeriesTimelineEntry{seriesTimelineEntry("crypto", dataset)})
		}
	}
	s.functions, s.datasets = content.Applied()
	s.check()
	return content, true, waiting
}

// writeTimeline 按 store 的规则写行：同一生效点的旧行先删再插。
func (s *planSim) writeTimeline(entries []store.SeriesTimelineEntry) {
	for _, entry := range entries {
		key := entry.DatasetID + "|" + entry.Frequency
		if froms := s.written[key]; len(froms) > 0 {
			require.GreaterOrEqual(s.t, entry.EffectiveFrom, froms[len(froms)-1], "%s 的时间线行生效点倒退", key)
		}
		s.written[key] = append(s.written[key], entry.EffectiveFrom)
		kept := s.timeline[:0]
		for _, old := range s.timeline {
			if old.DatasetID+"|"+old.Frequency != key || old.EffectiveFrom != entry.EffectiveFrom {
				kept = append(kept, old)
			}
		}
		s.timeline = append(kept, entry)
	}
}

// check 断言不变式：每一份的 hash 等于 Storage 在它 From 上的有效 hash；每个数据集至多两份、From 严格递增；
// 时间线重放没有结束早于开始的段。
func (s *planSim) check() {
	s.t.Helper()
	for key, dataset := range s.datasets {
		require.LessOrEqual(s.t, len(dataset.Copies), 2, key)
		for i, item := range dataset.Copies {
			if i > 0 {
				require.Greater(s.t, item.From, dataset.Copies[i-1].From, key)
			}
			hash, found := s.storage.effectiveHash(key, item.From)
			require.True(s.t, found, "%s 在 %s 上没有登记", key, time.Unix(item.From, 0).UTC())
			require.Equal(s.t, hash, item.Hash, "%s 第 %d 份的 hash 与 Storage 在它生效点上的有效 hash 不同", key, i)
		}
	}
	for key, segments := range klinecheck.BuildTimeline(s.timeline) {
		for _, segment := range segments {
			require.True(s.t, segment.To == 0 || segment.To > segment.From, "%s 出现结束早于开始的段 %+v", key, segment)
		}
	}
}

// rows 返回某个数据集的时间线行（按写入先后）。
func (s *planSim) rows(key string) []store.SeriesTimelineEntry {
	var out []store.SeriesTimelineEntry
	for _, entry := range s.timeline {
		if entry.DatasetID+"|"+entry.Frequency == key {
			out = append(out, entry)
		}
	}
	return out
}

// noHandoffs 是纯函数模拟用的交接截止结果：模拟里没有交接，两份截止总是相同，裁剪不看它。
type noHandoffs map[string]bool

func at(day, hour, minute, second int) time.Time {
	return time.Date(2026, 11, day, hour, minute, second, 0, time.UTC)
}

func simGroups(count int, frequencies ...string) []TaskGroup {
	return testGroups(count, frequencies...)
}

// 决策表逐行：K、S、R（登记与不登记）、A（新增、只改分配、停止）、W。
func TestDecisionTableRows(t *testing.T) {
	sim := newPlanSim(t, at(10, 1, 0, 0), simGroups(60, "5m"))
	const key = "ds_spot_5m|5m"

	// A（新增）：从零开始的数据集在 P_new 加一份并登记，时间线写一行。
	content, changed, _ := sim.step(planLeadTime)
	require.True(t, changed)
	first := content.Datasets[key]
	require.Equal(t, PlanActionAppend, first.Action)
	require.Equal(t, at(10, 1, 5, 0).Unix(), first.P)
	require.True(t, first.Register)
	require.True(t, first.Timeline)
	require.Len(t, sim.rows(key), 1)

	// K：什么都没变。
	sim.now = at(10, 1, 1, 0)
	_, changed, _ = sim.step(planLeadTime)
	require.False(t, changed)

	// R（只改分配，不登记）：头份还没开始（01:05 ≥ P_new 01:04），每函数标的数改小，hash 不变。
	sim.options.SymbolsPerFunction = 30
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	replaced := content.Datasets[key]
	require.Equal(t, PlanActionReplace, replaced.Action)
	require.Equal(t, first.P, replaced.P, "原地替换不重选 P")
	require.False(t, replaced.Register)
	require.False(t, replaced.Timeline, "序列与符号没变，不写行")
	require.Len(t, replaced.Copies, 1)

	// R（登记）：头份仍在未来，标的变了，同一生效点覆盖登记。
	sim.groups = simGroups(61, "5m")
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	replaced = content.Datasets[key]
	require.Equal(t, PlanActionReplace, replaced.Action)
	require.True(t, replaced.Register)
	require.True(t, replaced.Timeline)
	require.Len(t, sim.rows(key), 1, "同一生效点覆盖那一行")
	hash, _ := sim.storage.effectiveHash(key, first.P)
	require.Equal(t, replaced.Hash, hash, "Storage 在原生效点被覆盖")

	// S：只改符号。份不变，时间线在 Q = max(P_new, TimelineFrom) 写一行（头份在未来时并进那一行）。
	sim.groups[0].ExternalSymbols = map[string]string{"S001-USDT": "S001NEW"}
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	symbols := content.Datasets[key]
	require.Equal(t, PlanActionSymbols, symbols.Action)
	require.Equal(t, first.P, symbols.P, "头份在未来：并进头份那一行")
	require.False(t, symbols.Register)
	require.Len(t, symbols.Copies, 1)
	rows := sim.rows(key)
	require.Len(t, rows, 1)
	require.Equal(t, "S001NEW", rows[0].Symbols["S001-USDT"])

	// A（只改分配）：头份已过，hash 不变，加一份不登记、不写行。
	sim.now = at(10, 2, 0, 0)
	sim.options.SymbolsPerFunction = 20
	registered := sim.storage.calls
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	moved := content.Datasets[key]
	require.Equal(t, PlanActionAppend, moved.Action)
	require.False(t, moved.Register)
	require.False(t, moved.Timeline)
	require.Len(t, moved.Copies, 2)
	require.Equal(t, registered, sim.storage.calls)

	// W：两份都在、旧份还在窗口里时又变：本轮不动，给出等待原因。
	sim.now = at(10, 2, 4, 0) // P_new = 02:10，晚于头份 02:05：不能原地替换
	sim.groups = simGroups(62, "5m")
	sim.groups[0].ExternalSymbols = map[string]string{"S001-USDT": "S001NEW"} // 符号不变：例外表按标的记，符号变了函数也会随之下发
	_, changed, waiting := sim.step(planLeadTime)
	require.False(t, changed)
	require.Len(t, waiting, 1)
	require.Contains(t, waiting[0], "等旧份窗口结束")

	// 旧份过了窗口（后一份 From 02:05 + 60 秒 + 5 秒）：同一份计划里裁掉旧份并加一份。
	sim.now = at(10, 2, 6, 5)
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	require.True(t, content.Datasets[key].Trimmed)
	require.Equal(t, PlanActionAppend, content.Datasets[key].Action)
	require.Len(t, content.Datasets[key].Copies, 2)

	// A（停止）：头份已过，不再采集：在 Q 加一份停止份，hash 与数量取头份的，写停止行。
	sim.now = at(10, 3, 0, 0)
	_, changed, _ = sim.step(planLeadTime) // 先裁掉上一份的旧份
	require.True(t, changed)
	head := *sim.datasets[key].Head()
	sim.groups = nil
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	stop := content.Datasets[key]
	require.Equal(t, PlanActionAppend, stop.Action)
	require.True(t, stop.Stopped)
	require.Equal(t, DatasetCopy{From: at(10, 3, 5, 0).Unix(), Hash: head.Hash, Count: head.Count, LatenessS: head.LatenessS, Ingest: head.Ingest, Stop: true}, *stop.Head())
	require.False(t, stop.Register)
	require.Empty(t, sim.rows(key)[len(sim.rows(key))-1].Series, "停止行")

	// 停止份在生效点之前保留；过了生效点、旧份也过了窗口，数据集从已生效状态里消失。
	sim.now = at(10, 3, 4, 0)
	_, changed, _ = sim.step(planLeadTime)
	require.False(t, changed)
	sim.now = at(10, 3, 6, 5)
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	require.Nil(t, content.Datasets[key])
	require.Nil(t, sim.datasets[key])
	for _, function := range content.Functions {
		require.Nil(t, function.Target, "函数上没有数据集了，退役")
		require.True(t, function.Push)
	}
}

// 删了又马上加回：停止份在生效点之前保留，加回时原地替换。日线 [c0(H0), c1(H1@次日零点)] 当天停用，c1 换成停止份、保留 H1；
// 按 H0 加回时走原地替换并登记，Storage 在次日零点被覆盖成 H0（停止份若取 H0，就会误判成不用登记，次日起每期都 hash 不符）。
func TestStopThenReAddReplacesInPlaceAndOverridesStorage(t *testing.T) {
	const key = "ds_spot_1d|1d"
	sim := newPlanSim(t, at(9, 12, 0, 0), simGroups(60, "1d"))
	_, changed, _ := sim.step(planLeadTime) // H0 从 10 日零点起
	require.True(t, changed)
	h0 := sim.datasets[key].Head().Hash
	sim.now = at(10, 1, 0, 0)
	sim.groups = simGroups(61, "1d")
	_, changed, _ = sim.step(planLeadTime) // H1 从 11 日零点起
	require.True(t, changed)
	require.Len(t, sim.datasets[key].Copies, 2)
	h1 := sim.datasets[key].Head().Hash

	sim.now = at(10, 5, 0, 0)
	sim.groups = nil
	content, changed, _ := sim.step(planLeadTime)
	require.True(t, changed)
	stop := content.Datasets[key]
	require.Equal(t, PlanActionReplace, stop.Action)
	require.True(t, stop.Head().Stop)
	require.Equal(t, h1, stop.Head().Hash, "停止份保留被替换的头份的 hash")

	sim.now = at(10, 6, 0, 0)
	sim.groups = simGroups(60, "1d")
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	readded := content.Datasets[key]
	require.Equal(t, PlanActionReplace, readded.Action)
	require.True(t, readded.Register, "停止份记着 H1，按 H0 加回要登记")
	require.Equal(t, h0, readded.Head().Hash)
	hash, _ := sim.storage.effectiveHash(key, at(11, 0, 0, 0).Unix())
	require.Equal(t, h0, hash, "Storage 在次日零点被覆盖成 H0")
	rows := sim.rows(key)
	require.Equal(t, at(11, 0, 0, 0).Unix(), rows[len(rows)-1].EffectiveFrom)
	require.NotEmpty(t, rows[len(rows)-1].Series, "次日零点那一行换回序列")
}

// 裁剪：两份截止不同时窗口取大的那个，并且要求本轮交接截止已改成功。
func TestTrimUsesTheLargerLatenessAndWaitsForTheLatenessCall(t *testing.T) {
	from := at(10, 1, 10, 0).Unix()
	prev := &PlanDataset{PlanDatasetState: PlanDatasetState{DatasetID: "ds", Frequency: "1m", Hash: "h1", Count: 1, LatenessS: 5, Ingest: ingestStream},
		Copies: []DatasetCopy{{From: from - 600, Hash: "h0", Count: 1, LatenessS: 15, Ingest: ingestSCF}, {From: from, Hash: "h1", Count: 1, LatenessS: 5, Ingest: ingestStream}}}
	decide := func(now time.Time, ok bool) *PlanDataset {
		dataset, _, err := decideDataset("ds|1m", prev, nil, nil, nil, PlanTiming{Now: now, Lead: planLeadTime, LatenessOK: map[string]bool{"ds|1m": ok}})
		require.NoError(t, err)
		return dataset
	}
	require.False(t, decide(time.Unix(from+19, 0), true).Trimmed, "From + 15 + 5 之前不裁（取两份截止里大的）")
	require.False(t, decide(time.Unix(from+20, 0), false).Trimmed, "截止还没改成功，不裁")
	require.True(t, decide(time.Unix(from+20, 0), true).Trimmed)
}

// 验收场景（A 股）：空间带 5m 与 1d。01:05 第一次成员变化：5m 在 01:10 加份，1d 在次日零点加份，计划当轮生效；
// 01:10 + 截止 + 5 秒之后的第一轮出现只裁 5m 旧份的计划；04:05 第二次变化：5m 在 04:10 加份，1d 头份仍在未来、原地替换，
// Storage 被覆盖，时间线次日零点那一行被替换。
func TestAShareScenarioAdvancesEachDatasetOnItsOwn(t *testing.T) {
	const minute5, daily = "ds_spot_5m|5m", "ds_spot_1d|1d"
	sim := newPlanSim(t, at(9, 12, 0, 0), simGroups(60, "5m", "1d"))
	_, changed, _ := sim.step(planLeadTime)
	require.True(t, changed)
	sim.now = at(10, 0, 30, 0)
	sim.step(planLeadTime)
	require.Len(t, sim.datasets[minute5].Copies, 1)
	require.Len(t, sim.datasets[daily].Copies, 1)

	// 01:05 第一次成员变化。
	sim.now = at(10, 1, 5, 0)
	sim.groups = simGroups(61, "5m", "1d")
	content, changed, _ := sim.step(planLeadTime)
	require.True(t, changed)
	require.Equal(t, PlanActionAppend, content.Datasets[minute5].Action)
	require.Equal(t, at(10, 1, 10, 0).Unix(), content.Datasets[minute5].P)
	require.Equal(t, PlanActionAppend, content.Datasets[daily].Action)
	require.Equal(t, at(11, 0, 0, 0).Unix(), content.Datasets[daily].P)
	// 01:05～01:10 + 迟到窗口之间，旧周期的迟到提交用的旧份仍在函数计划里。
	for _, function := range content.Functions {
		dataset := findPlanDataset(function.Target, minute5)
		require.Len(t, dataset.Copies, 2)
		old, ok := dataset.SelectCopy(at(10, 1, 5, 0))
		require.True(t, ok)
		require.Equal(t, content.Datasets[minute5].Copies[0].Hash, old.Hash)
	}

	// 01:11 还在窗口里；01:12 那一轮只裁 5m 旧份。
	sim.now = at(10, 1, 11, 0)
	_, changed, _ = sim.step(planLeadTime)
	require.False(t, changed)
	sim.now = at(10, 1, 12, 0)
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	require.True(t, content.Datasets[minute5].Trimmed)
	require.Equal(t, PlanActionKeep, content.Datasets[minute5].Action)
	require.Equal(t, PlanActionKeep, content.Datasets[daily].Action)
	require.False(t, content.Datasets[daily].Trimmed)
	require.Len(t, content.Datasets[daily].Copies, 2)

	// 04:05 第二次变化。
	sim.now = at(10, 4, 5, 0)
	sim.groups = simGroups(62, "5m", "1d")
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	require.Equal(t, PlanActionAppend, content.Datasets[minute5].Action)
	require.Equal(t, at(10, 4, 10, 0).Unix(), content.Datasets[minute5].P)
	require.Equal(t, PlanActionReplace, content.Datasets[daily].Action)
	require.Equal(t, at(11, 0, 0, 0).Unix(), content.Datasets[daily].P)
	require.True(t, content.Datasets[daily].Register)
	hash, _ := sim.storage.effectiveHash(daily, at(11, 0, 0, 0).Unix())
	require.Equal(t, content.Datasets[daily].Hash, hash, "Storage 在次日零点被覆盖")
	rows := sim.rows(daily)
	require.Equal(t, at(11, 0, 0, 0).Unix(), rows[len(rows)-1].EffectiveFrom)
	require.Len(t, rows[len(rows)-1].Series, 62, "次日零点那一行被替换")
	count := 0
	for _, row := range rows {
		if row.EffectiveFrom == at(11, 0, 0, 0).Unix() {
			count++
		}
	}
	require.Equal(t, 1, count)
}

// R 与 W 的分界：01:05 在 01:10 加份之后，01:06 又变（P_new = 01:10 = 头份 From）走原地替换；01:09 又变（P_new = 01:15，
// 旧份未过期）等待；旧份过期后的那一轮在同一份计划里裁掉旧份、在 01:15 加份。
func TestReplaceAndWaitBoundary(t *testing.T) {
	const key = "ds_spot_5m|5m"
	sim := newPlanSim(t, at(10, 0, 0, 0), simGroups(60, "5m"))
	sim.step(planLeadTime)
	sim.now = at(10, 1, 5, 0)
	sim.groups = simGroups(61, "5m")
	content, _, _ := sim.step(planLeadTime)
	require.Equal(t, PlanActionAppend, content.Datasets[key].Action)
	require.Equal(t, at(10, 1, 10, 0).Unix(), content.Datasets[key].P)

	sim.now = at(10, 1, 6, 0)
	sim.groups = simGroups(62, "5m")
	content, changed, _ := sim.step(planLeadTime)
	require.True(t, changed)
	require.Equal(t, PlanActionReplace, content.Datasets[key].Action)
	require.Equal(t, at(10, 1, 10, 0).Unix(), content.Datasets[key].P)

	sim.now = at(10, 1, 9, 0)
	sim.groups = simGroups(63, "5m")
	_, changed, waiting := sim.step(planLeadTime)
	require.False(t, changed, "本轮不建计划")
	require.NotEmpty(t, waiting)

	sim.now = at(10, 1, 11, 5)
	content, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	require.True(t, content.Datasets[key].Trimmed)
	require.Equal(t, PlanActionAppend, content.Datasets[key].Action)
	require.Equal(t, at(10, 1, 15, 0).Unix(), content.Datasets[key].P)
}

// cron 是各数据集的并集：5m 与 1d 同函数，5m 移走、1d 留下时，cron 在 5m 旧份过窗口后才变成日线的。
func TestCronFollowsTheUnionOfDatasetsWithOldCopies(t *testing.T) {
	sim := newPlanSim(t, at(9, 12, 0, 0), simGroups(40, "5m", "1d"))
	sim.step(planLeadTime)
	sim.now = at(10, 1, 0, 0)
	sim.step(planLeadTime)
	cron := func() string {
		require.Len(t, sim.functions, 1)
		for _, function := range sim.functions {
			return function.Cron
		}
		return ""
	}
	require.Equal(t, "0 */5 * * * * *", cron())

	sim.groups = simGroups(40, "1d")
	_, changed, _ := sim.step(planLeadTime)
	require.True(t, changed)
	require.Equal(t, "0 */5 * * * * *", cron(), "5m 旧份还在，cron 不变")
	sim.now = at(10, 1, 6, 5)
	_, changed, _ = sim.step(planLeadTime)
	require.True(t, changed)
	require.Nil(t, sim.datasets["ds_spot_5m|5m"])
	require.Equal(t, "0 0 * * * * *", cron(), "5m 旧份过窗口后变成日线的")
}

// 单调性与登记不变式的性质测试：随机的成员变化、改符号、删除与加回，每轮随机取提前量（30 秒、2、3、6 分钟）、随机推进时间。
// 每一轮都检查：每一份的 hash 等于 Storage 在它生效点上的有效 hash、登记从不被拒、时间线行生效点不倒退、重放没有结束早于开始的段、
// 下发的函数计划都通过 Validate。
func TestPlanSequencesKeepTimelineMonotonicAndStorageConsistent(t *testing.T) {
	leads := []time.Duration{planLeadTimePushOnly, planLeadTime, planLeadTimeHandoff, planLeadTimeWithCreate}
	steps := []time.Duration{20 * time.Second, time.Minute, 3 * time.Minute, 10 * time.Minute, time.Hour, 7 * time.Hour}
	seen := map[string]int{}
	for seed := int64(1); seed <= 40; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			random := rand.New(rand.NewSource(seed))
			sim := newPlanSim(t, at(10, 0, 0, 7), simGroups(5, "5m", "1d"))
			count := 5
			present := map[string]bool{"5m": true, "1d": true}
			symbols := map[string]string{}
			for round := 0; round < 80; round++ {
				switch random.Intn(6) {
				case 0, 1:
					count = 3 + random.Intn(5)
				case 2:
					symbols = map[string]string{"S001-USDT": fmt.Sprintf("S001V%d", random.Intn(3))}
				case 3:
					frequency := []string{"5m", "1d"}[random.Intn(2)]
					present[frequency] = !present[frequency]
				}
				var frequencies []string
				for _, frequency := range []string{"5m", "1d"} {
					if present[frequency] {
						frequencies = append(frequencies, frequency)
					}
				}
				sim.groups = simGroups(count, frequencies...)
				for i := range sim.groups {
					sim.groups[i].ExternalSymbols = symbols
				}
				content, changed, waiting := sim.step(leads[random.Intn(len(leads))])
				if len(waiting) > 0 {
					seen[PlanActionWait]++
				}
				if changed {
					for _, dataset := range content.Datasets {
						seen[dataset.Action]++
						if dataset.Stopped {
							seen["stop"]++
						}
						if dataset.Trimmed {
							seen["trim"]++
						}
					}
				}
				sim.now = sim.now.Add(steps[random.Intn(len(steps))])
			}
			keys := make([]string, 0, len(sim.written))
			for key := range sim.written {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			require.NotEmpty(t, keys)
		})
	}
	for _, kind := range []string{PlanActionAppend, PlanActionReplace, PlanActionSymbols, PlanActionWait, "stop", "trim"} {
		require.Positive(t, seen[kind], "随机序列没有覆盖到 %s", kind)
	}
}

// 原地替换在 collector-stream 上自然生效：闸门在收盘时才为那一期 SelectCopy，换计划后替换生效点那一期用的是新份。
func TestStreamPlanAfterReplaceSelectsTheNewCopy(t *testing.T) {
	h := newIngestHarness(t, ingestStream, "1h")
	h.settle(true)
	h.setSubjects(61)
	require.NoError(t, h.reconcile(h.r))
	open := h.open()
	require.NotNil(t, open)
	first := h.content(open).Datasets["ds_spot_1h|1h"]
	h.workers.apply(open.ID)
	require.NoError(t, h.reconcile(h.r))
	require.Nil(t, h.open())

	h.setSubjects(62)
	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(h.r))
	replace := h.open()
	require.NotNil(t, replace)
	dataset := h.content(replace).Datasets["ds_spot_1h|1h"]
	require.Equal(t, PlanActionReplace, dataset.Action)
	require.Equal(t, first.P, dataset.P)
	progress, err := decodeFetchPlanProgress(replace.Progress)
	require.NoError(t, err)
	require.True(t, progress.StreamPushed)

	id, items, err := StreamPlanState(replace, nil, false)
	require.NoError(t, err)
	require.Equal(t, replace.ID, id, "拉取只按计划号")
	require.Len(t, items, 1)
	copy, ok := findPlanDataset(items[0].Plan, "ds_spot_1h|1h").SelectCopy(time.Unix(first.P, 0))
	require.True(t, ok)
	require.Equal(t, dataset.Hash, copy.Hash, "替换生效点那一期用的是新份")
	require.Len(t, copy.Subjects, 62)
}

// 撤回：两个数据集一起变，其中一个登记返回 effective_from_stale。它被撤回，另一个照常生效。
// 只剩它一个时未生效记录被删掉，见 TestPlanReconcilerWithdrawsADatasetOnExplicitStaleRejection。
func TestWithdrawKeepsTheOtherDataset(t *testing.T) {
	h := newIngestHarness(t, ingestSCF, "1m", "1h")
	h.settle(true)
	h.setSubjects(61)
	h.registrar.stale = 1 // 第一个登记（按键排序是 1h）被拒
	require.NoError(t, h.reconcile(h.r))
	require.Contains(t, h.r.Issue(), "已撤回")
	require.NoError(t, h.reconcile(h.r))
	require.Nil(t, h.open())
	withdrawn := h.content(h.lastRecord())
	require.Equal(t, PlanActionKeep, withdrawn.Datasets["ds_spot_1h|1h"].Action, "1h 撤回")
	require.Len(t, withdrawn.Datasets["ds_spot_1h|1h"].Copies, 1)
	require.Equal(t, PlanActionAppend, withdrawn.Datasets["ds_spot_1m|1m"].Action, "1m 照常生效")
	for _, function := range withdrawn.Functions {
		if function.Target != nil {
			require.Len(t, findPlanDataset(function.Target, "ds_spot_1h|1h").Copies, 1, "函数 Target 随之重新组合")
		}
	}

	// 下一轮重新查表：1h 在新的生效点登记。
	h.now = h.now.Add(20 * time.Second)
	require.NoError(t, h.reconcile(h.r))
	require.Equal(t, PlanActionAppend, h.content(h.lastRecord()).Datasets["ds_spot_1h|1h"].Action)
}

// 崩溃重放：登记请求发出、Storage 已经登记，但 Collector 落库前重启；下一轮用同一个请求重放，Storage 返回成功，内容不改。
func TestRegistrationReplayAfterACrashKeepsTheContent(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.registrar.lostReplies = 1
	r := h.reconciler()
	require.Error(t, h.reconcile(r), "回复丢了，按结果不明处理")
	open := h.open()
	require.NotNil(t, open)
	p := h.content(open).Datasets["ds_spot|1m"].P

	h.now = h.now.Add(20 * time.Second)
	restarted := h.reconciler()
	require.NoError(t, h.reconcile(restarted))
	require.Nil(t, h.open())
	require.Equal(t, p, h.content(h.lastRecord()).Datasets["ds_spot|1m"].P, "重放同一个请求，内容不改")
	require.Equal(t, []int64{p, p}, h.registrar.registered["ds_spot|1m"], "同 P 同 hash 的重放成功")
}
