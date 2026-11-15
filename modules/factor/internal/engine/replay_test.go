package engine

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type replayTasks struct{ set domain.EngineTask }

func (r replayTasks) SetByID(id string) (domain.EngineTask, SetLookup) {
	if id == r.set.Set.TaskID {
		return r.set, SetFound
	}
	return domain.EngineTask{}, SetGone
}

type replaySource struct{}

func (replaySource) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close", "open"}, nil
}

func (replaySource) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"A", "B", "GONE"}, nil
}

var rt0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func newReplayManagerForTest(t *testing.T) (*replayManager, domain.EngineTask) {
	t.Helper()
	return newReplayManagerWithTask(t, uncompiled(engineTask("ftask_replay", "dataset_a", true, "bias")))
}

func newReplayManagerWithTask(t *testing.T, set domain.EngineTask) (*replayManager, domain.EngineTask) {
	t.Helper()
	store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	fields := []seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64}, {Name: "open", Type: seriesstore.TypeFloat64}}
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
	write := func(subject string, minutes int) {
		cols := seriesstore.NewColumns(fields, minutes)
		for i := 0; i < minutes; i++ {
			price := 100 + float64(i)
			cols.AppendRow(rt0.Add(time.Duration(i)*time.Minute), plainTag, false, seriesstore.Float(price), seriesstore.Float(price-0.5))
		}
		_, err := store.WriteRange(context.Background(), src, seriesstore.SubjectWrite{
			Subject: subject, From: rt0, To: rt0.Add(time.Duration(minutes) * time.Minute), Rows: cols,
		}, seriesstore.WriteOptions{FillGaps: true})
		require.NoError(t, err)
	}
	write("A", 20)
	write("B", 20)
	write("GONE", 8) // 数据到不了回放的最后一期：被排除
	runner := pipeline.NewRunner(nil, nil, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
	return newReplayManager(ReplayDeps{Sets: replayTasks{set: set}, Source: replaySource{}, Store: store, Runner: runner}), set
}

func replayMinute(minute int) string {
	return rt0.Add(time.Duration(minute) * time.Minute).Format(time.RFC3339)
}

func replayMinuteUnix(minute int) int64 { return rt0.Add(time.Duration(minute) * time.Minute).Unix() }

// replayOpenReq 是测试用的建会话请求：分钟数相对 rt0。
func replayOpenReq(id string, from, to, extra int, columns ...string) *factorpb.OpenReplayReq {
	return &factorpb.OpenReplayReq{ReplayId: id, TaskId: "ftask_replay", From: replayMinute(from), To: replayMinute(to),
		ExtraPeriods: int32(extra), Columns: columns}
}

func openReplay(t *testing.T, m *replayManager, req *factorpb.OpenReplayReq) *factorpb.OpenReplayRsp {
	t.Helper()
	rsp, err := replayService{m: m}.OpenReplay(context.Background(), req)
	require.NoError(t, err, "失败只放在 ret_info 里")
	return rsp
}

func mustOpenReplay(t *testing.T, m *replayManager, req *factorpb.OpenReplayReq) *factorpb.OpenReplayRsp {
	t.Helper()
	rsp := openReplay(t, m, req)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	return rsp
}

func stepReplay(t *testing.T, m *replayManager, req *factorpb.StepReplayReq) *factorpb.StepReplayRsp {
	t.Helper()
	rsp, err := replayService{m: m}.StepReplay(context.Background(), req)
	require.NoError(t, err, "失败只放在 ret_info 里")
	return rsp
}

// mustStepOne 算一期并返回这一期的帧。
func mustStepOne(t *testing.T, m *replayManager, id string, minute int, params ...*factorpb.MemberParams) *factorpb.ReplayPeriod {
	t.Helper()
	rsp := stepReplay(t, m, &factorpb.StepReplayReq{ReplayId: id, PeriodTime: replayMinuteUnix(minute), Periods: 1, Params: params})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetPeriods(), 1)
	return rsp.GetPeriods()[0]
}

// replayRowOf 取一期里某条序列的行；没有时返回 nil。
func replayRowOf(period *factorpb.ReplayPeriod, subject, tag string) *factorpb.ReplayRow {
	for _, row := range period.GetRows() {
		if row.GetSubjectId() == subject && row.GetSeriesTag() == tag {
			return row
		}
	}
	return nil
}

func replayHasSubject(period *factorpb.ReplayPeriod, subject string) bool {
	for _, row := range period.GetRows() {
		if row.GetSubjectId() == subject {
			return true
		}
	}
	return false
}

// 回放逐期取因子帧：预热区间用于回看，数据到不了最后一期的标的被排除；行按（标的, series_tag）给出请求的列、上一期的同名列与年龄。
func TestReplayOpenAndStep(t *testing.T) {
	m, _ := newReplayManagerForTest(t)
	open := mustOpenReplay(t, m, replayOpenReq("r1", 5, 10, 1, "bias", "close"))
	require.Equal(t, []string{"A", "B"}, open.GetSubjects())
	require.Len(t, open.GetExcluded(), 1)
	require.Equal(t, "GONE", open.GetExcluded()[0].GetSubjectId())
	require.Equal(t, []int64{replayMinuteUnix(5), replayMinuteUnix(6), replayMinuteUnix(7), replayMinuteUnix(8), replayMinuteUnix(9)}, open.GetPeriodTimes())
	require.Equal(t, int64(60), open.GetStepSeconds())
	require.Len(t, open.GetFactors(), 1)
	require.Equal(t, []string{"bias"}, open.GetFactors()[0].GetOutputs())

	period := mustStepOne(t, m, "r1", 5)
	require.Equal(t, replayMinuteUnix(5), period.GetPeriodTime())
	a := replayRowOf(period, "A", plainTag)
	require.NotNil(t, a)
	require.Equal(t, map[string]float64{"close": 105, "bias": 104.5}, a.GetValues())
	require.Equal(t, map[string]float64{"close": 104, "bias": 103.5}, a.GetPrevious(), "上一期的同名列也要带回")
	require.Empty(t, a.GetParams(), "没有请求 __params 时不带参数")
	require.Equal(t, int32(6), a.GetAgeBars())
	require.Equal(t, "complete", period.GetStates()["bias"].GetStatus())
	require.False(t, replayHasSubject(period, "GONE"))

	// 同一期重复请求得到同样的帧；区间外的周期被拒绝。
	again := mustStepOne(t, m, "r1", 5)
	require.True(t, proto.Equal(period, again))
	rsp := stepReplay(t, m, &factorpb.StepReplayReq{ReplayId: "r1", PeriodTime: replayMinuteUnix(10), Periods: 1})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	rsp = stepReplay(t, m, &factorpb.StepReplayReq{ReplayId: "r1", PeriodTime: replayMinuteUnix(5)})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "periods 至少为 1")

	closed, err := replayService{m: m}.CloseReplay(context.Background(), &factorpb.CloseReplayReq{ReplayId: "r1"})
	require.NoError(t, err)
	require.True(t, closed.GetClosed())
	closed, err = replayService{m: m}.CloseReplay(context.Background(), &factorpb.CloseReplayReq{ReplayId: "r1"})
	require.NoError(t, err)
	require.False(t, closed.GetClosed(), "已经关闭的会话")
	rsp = stepReplay(t, m, &factorpb.StepReplayReq{ReplayId: "r1", PeriodTime: replayMinuteUnix(5), Periods: 1})
	require.Equal(t, commonpb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode(), "会话不存在时调用方要重新建立")
}

func TestReplayOpenValidation(t *testing.T) {
	m, _ := newReplayManagerForTest(t)
	unaligned := replayOpenReq("r2", 5, 10, 0, "bias")
	unaligned.From = rt0.Add(5*time.Minute + time.Second).Format(time.RFC3339)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, openReplay(t, m, unaligned).GetRetInfo().GetCode(), "没有对齐的区间被拒绝")
	require.Equal(t, commonpb.ErrorCode_CONFLICT, openReplay(t, m, replayOpenReq("r2", 5, 30, 0, "bias")).GetRetInfo().GetCode(), "本地数据到不了最后一期")
	missing := replayOpenReq("r2", 0, 1, 0, "bias")
	missing.TaskId = "missing"
	require.Equal(t, commonpb.ErrorCode_NOT_FOUND, openReplay(t, m, missing).GetRetInfo().GetCode())
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, openReplay(t, m, replayOpenReq("r2", 5, 10, 0)).GetRetInfo().GetCode(), "columns 必填")
	unknown := openReplay(t, m, replayOpenReq("r2", 5, 10, 0, "bias", "volume"))
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, unknown.GetRetInfo().GetCode())
	require.Contains(t, unknown.GetRetInfo().GetMsg(), "volume")

	// 只需要覆盖到回放的最后一期：本地数据到第 19 分钟，回放到 [.., 20) 可以建立（不再要终点之后的一根）。
	open := mustOpenReplay(t, m, replayOpenReq("r3", 15, 20, 0, "bias"))
	require.Equal(t, []string{"A", "B"}, open.GetSubjects())
	require.Equal(t, replayMinuteUnix(19), open.GetPeriodTimes()[len(open.GetPeriodTimes())-1])
	require.NotNil(t, replayRowOf(mustStepOne(t, m, "r3", 19), "A", plainTag))
}

// 未来才上市的标的不能改变早期周期的截面：本期的全集只含已经上市的标的。
func TestReplayUniverseExcludesNotYetListedSubjects(t *testing.T) {
	m, _ := newReplayManagerForTest(t)
	ctx := context.Background()
	// 用一个在第 8 分钟才上市的标的 LATE：数据覆盖到终点。
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
	fields := []seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64}, {Name: "open", Type: seriesstore.TypeFloat64}}
	cols := seriesstore.NewColumns(fields, 12)
	for i := 8; i < 20; i++ {
		cols.AppendRow(rt0.Add(time.Duration(i)*time.Minute), plainTag, false, seriesstore.Float(50), seriesstore.Float(50))
	}
	_, err := m.deps.Store.WriteRange(ctx, src, seriesstore.SubjectWrite{Subject: "LATE", From: rt0.Add(8 * time.Minute), To: rt0.Add(20 * time.Minute), Rows: cols}, seriesstore.WriteOptions{})
	require.NoError(t, err)
	m.deps.Source = lateSource{}
	mustOpenReplay(t, m, replayOpenReq("r9", 5, 10, 1, "bias"))

	early := mustStepOne(t, m, "r9", 6)
	require.False(t, replayHasSubject(early, "LATE"), "第 6 分钟 LATE 还没上市")
	require.True(t, replayHasSubject(early, "A"))
	later := mustStepOne(t, m, "r9", 8)
	require.Equal(t, int32(1), replayRowOf(later, "LATE", plainTag).GetAgeBars(), "上市当期是第 1 根")
}

type lateSource struct{ replaySource }

// 本地的填充行（同步补的缺失周期：OHLC 取上一根收盘）与实时一样不算本期有行：HOLE 第 10、11 分钟是填充行，这两期不出行、
// 也不算本期可用；第 12 分钟恢复真实行后照常出行。
func TestReplaySkipsFilledRows(t *testing.T) {
	m, _ := newReplayManagerForTest(t)
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
	fields := []seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64}, {Name: "open", Type: seriesstore.TypeFloat64}}
	cols := seriesstore.NewColumns(fields, 18)
	for i := 0; i < 20; i++ {
		if i == 10 || i == 11 {
			continue
		}
		cols.AppendRow(rt0.Add(time.Duration(i)*time.Minute), plainTag, false, seriesstore.Float(200+float64(i)), seriesstore.Float(200+float64(i)))
	}
	_, err := m.deps.Store.WriteRange(context.Background(), src, seriesstore.SubjectWrite{Subject: "HOLE", From: rt0, To: rt0.Add(20 * time.Minute), Rows: cols},
		seriesstore.WriteOptions{FillGaps: true})
	require.NoError(t, err)
	m.deps.Source = holeSource{}
	mustOpenReplay(t, m, replayOpenReq("r_hole", 8, 14, 1, "bias", "close"))
	hole := m.session("r_hole").view.Series("HOLE")
	require.Len(t, hole, 1)
	require.True(t, hole[0].Filled[(replayMinuteUnix(10)-hole[0].Start)/60], "夹具：第 10 分钟是同步补的填充行")
	require.NotNil(t, replayRowOf(mustStepOne(t, m, "r_hole", 9), "HOLE", plainTag))
	for _, minute := range []int{10, 11} {
		period := mustStepOne(t, m, "r_hole", minute)
		require.False(t, replayHasSubject(period, "HOLE"), "第 %d 分钟是填充行，不出行", minute)
		require.NotNil(t, replayRowOf(period, "A", plainTag))
	}
	back := replayRowOf(mustStepOne(t, m, "r_hole", 12), "HOLE", plainTag)
	require.NotNil(t, back, "恢复真实行后照常出行")
	require.Equal(t, 212.0, back.GetValues()["close"])
}

type holeSource struct{ replaySource }

func (holeSource) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"A", "B", "HOLE"}, nil
}

func (lateSource) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"A", "B", "LATE"}, nil
}

// 多数标的的本地数据不够预热区间：因子前段是空值，回放没有意义，拒绝建立会话并提示先补齐本地历史；显式允许后建立并报告不足的标的数。
func TestReplayOpenRejectsShortWarmupUnlessAllowed(t *testing.T) {
	m, _ := newReplayManagerForTest(t)
	req := replayOpenReq("r10", 1, 5, 1, "bias")
	rsp := openReplay(t, m, req)
	require.Equal(t, commonpb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "请先补齐本地历史再发起")
	require.NotContains(t, rsp.GetRetInfo().GetMsg(), "确认接受短预热", "策略侧没有放行短预热的选项")

	req.AllowShortWarmup = true
	rsp = mustOpenReplay(t, m, req)
	require.Equal(t, int32(3), rsp.GetShortWarmup(), "A、B、GONE 都从第 0 分钟起，早于预热起点的数据都不存在")
}

// 会话建立期间固定任务引用的模块，登记之后释放；固定之后目录已经切换版本的，要求重试。
func TestReplayOpenPinsModulesAndRejectsAConcurrentVersionSwitch(t *testing.T) {
	m, set := newReplayManagerForTest(t)
	pins, releases := 0, 0
	m.deps.Pin = func(domain.EngineTask) func() { pins++; return func() { releases++ } }
	mustOpenReplay(t, m, replayOpenReq("rp", 5, 10, 1, "bias"))
	require.Equal(t, 1, pins)
	require.Equal(t, 1, releases, "登记之后释放")

	// 固定之后目录里的任务引用的版本变了：拒绝，也不泄漏固定。
	switched := set
	switched.Members = append([]domain.MemberDef(nil), set.Members...)
	switched.Members[0].Factor.UDFs = map[string]factorexpr.UDF{"x": {Name: "x", Hash: "ab"}}
	m.deps.Sets = &switchingTasks{first: set, then: switched}
	require.Equal(t, commonpb.ErrorCode_CONFLICT, openReplay(t, m, replayOpenReq("rp2", 5, 10, 1, "bias")).GetRetInfo().GetCode())
	require.Equal(t, 2, pins)
	require.Equal(t, 2, releases)
}

// switchingTasks 第一次查询返回 first，之后返回 then。
type switchingTasks struct {
	first, then domain.EngineTask
	calls       int
}

func (s *switchingTasks) SetByID(string) (domain.EngineTask, SetLookup) {
	s.calls++
	if s.calls == 1 {
		return s.first, SetFound
	}
	return s.then, SetFound
}

// 按请求的列裁剪：只请求源数据列时没有因子列与参数，只请求因子列时没有源数据列；__params 单独请求。
func TestReplayStepTrimsRowsToRequestedColumns(t *testing.T) {
	m, _ := newReplayManagerForTest(t)
	mustOpenReplay(t, m, replayOpenReq("src", 5, 10, 1, "open"))
	a := replayRowOf(mustStepOne(t, m, "src", 5), "A", plainTag)
	require.Equal(t, map[string]float64{"open": 104.5}, a.GetValues())
	require.Equal(t, map[string]float64{"open": 103.5}, a.GetPrevious())
	require.Empty(t, a.GetParams())

	mustOpenReplay(t, m, replayOpenReq("factor", 5, 10, 1, "bias", factorname.ParamsColumn, "bias"))
	a = replayRowOf(mustStepOne(t, m, "factor", 5), "A", plainTag)
	require.Equal(t, map[string]float64{"bias": 104.5}, a.GetValues(), "重复的列只出一次")
	require.Equal(t, map[string]float64{"bias": 103.5}, a.GetPrevious())
	require.Contains(t, a.GetParams(), "bias", "请求了 __params 时带上各成员的参数（没有参数的成员是空表）")
	require.Equal(t, int32(6), a.GetAgeBars(), "年龄与请求的列无关")
}

// 一步多期：一次算 5 期与逐期请求得到的帧逐个相同；请求的期数超过区间时到区间末尾为止。
func TestReplayMultiPeriodStepMatchesSingleSteps(t *testing.T) {
	single, _ := newReplayManagerForTest(t)
	mustOpenReplay(t, single, replayOpenReq("one", 5, 10, 1, "bias", "close", factorname.ParamsColumn))
	var want []*factorpb.ReplayPeriod
	for minute := 5; minute < 10; minute++ {
		want = append(want, mustStepOne(t, single, "one", minute))
	}

	batch, _ := newReplayManagerForTest(t)
	mustOpenReplay(t, batch, replayOpenReq("many", 5, 10, 1, "bias", "close", factorname.ParamsColumn))
	rsp := stepReplay(t, batch, &factorpb.StepReplayReq{ReplayId: "many", PeriodTime: replayMinuteUnix(5), Periods: 100})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetPeriods(), len(want), "到区间末尾为止")
	for i := range want {
		require.True(t, proto.Equal(want[i], rsp.GetPeriods()[i]), "第 %d 期", i)
	}
}

// 单步响应不超过上限：放不下时只返回前面几期（至少 1 期），一期就放不下时拒绝并提示减少列。
func TestReplayStepStaysWithinTheSizeLimit(t *testing.T) {
	m, _ := newReplayManagerForTest(t)
	mustOpenReplay(t, m, replayOpenReq("size", 5, 10, 1, "bias", "close", "open", factorname.ParamsColumn))
	one := proto.Size(mustStepOne(t, m, "size", 5)) + 10

	m.maxStepBytes = 2*one + one/2
	rsp := stepReplay(t, m, &factorpb.StepReplayReq{ReplayId: "size", PeriodTime: replayMinuteUnix(5), Periods: 5})
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetPeriods(), 2, "只放得下两期")
	require.Equal(t, replayMinuteUnix(5), rsp.GetPeriods()[0].GetPeriodTime())
	require.Equal(t, replayMinuteUnix(6), rsp.GetPeriods()[1].GetPeriodTime(), "从请求的期起连续")
	require.LessOrEqual(t, proto.Size(rsp), m.maxStepBytes)

	// 默认上限 3 MiB 远小于服务的包体上限 4 MiB。
	require.Equal(t, 3<<20, newReplayManager(ReplayDeps{}).maxStepBytes)

	m.maxStepBytes = one / 2
	rsp = stepReplay(t, m, &factorpb.StepReplayReq{ReplayId: "size", PeriodTime: replayMinuteUnix(5), Periods: 5})
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "减少引用的列")
	require.Empty(t, rsp.GetPeriods())
}

// 统一数据集：同一个标的的现货、永续两条序列各自出行，值与年龄按序列给；永续晚上市，上市之前只有现货一行。
func TestReplayUnifiedDatasetEmitsOneRowPerSeries(t *testing.T) {
	m, _ := newReplayManagerForTest(t)
	ctx := context.Background()
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_u", Freq: "1m"}
	fields := []seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64}, {Name: "open", Type: seriesstore.TypeFloat64}}
	// 一次写入整段（按时间、tag 排序）：现货从第 0 分钟起、基数 100，永续从第 7 分钟起、基数 200。
	cols := seriesstore.NewColumns(fields, 40)
	for i := 0; i < 20; i++ {
		at := rt0.Add(time.Duration(i) * time.Minute)
		cols.AppendRow(at, "spot", false, seriesstore.Float(100+float64(i)), seriesstore.Float(100+float64(i)-0.5))
		if i >= 7 {
			cols.AppendRow(at, "swap", false, seriesstore.Float(200+float64(i)), seriesstore.Float(200+float64(i)-0.5))
		}
	}
	_, err := m.deps.Store.WriteRange(ctx, src, seriesstore.SubjectWrite{Subject: "A", From: rt0, To: rt0.Add(20 * time.Minute), Rows: cols},
		seriesstore.WriteOptions{FillGaps: true})
	require.NoError(t, err)
	set := uncompiled(engineTask("ftask_replay", "dataset_u", true, "bias"))
	m.deps.Sets = replayTasks{set: set}
	m.deps.Source = unifiedSource{}
	mustOpenReplay(t, m, replayOpenReq("u", 5, 10, 1, "bias", "close"))

	early := mustStepOne(t, m, "u", 6)
	require.Len(t, early.GetRows(), 1, "第 6 分钟永续还没上市")
	spot := replayRowOf(early, "A", "spot")
	require.Equal(t, map[string]float64{"close": 106, "bias": 105.5}, spot.GetValues())
	require.Equal(t, int32(7), spot.GetAgeBars())

	later := mustStepOne(t, m, "u", 9)
	require.Len(t, later.GetRows(), 2)
	spot, swap := replayRowOf(later, "A", "spot"), replayRowOf(later, "A", "swap")
	require.Equal(t, map[string]float64{"close": 109, "bias": 108.5}, spot.GetValues())
	require.Equal(t, map[string]float64{"close": 108, "bias": 107.5}, spot.GetPrevious())
	require.Equal(t, map[string]float64{"close": 209, "bias": 208.5}, swap.GetValues(), "两条序列各自的值，不是同一个")
	require.Equal(t, map[string]float64{"close": 208, "bias": 207.5}, swap.GetPrevious())
	require.Equal(t, int32(10), spot.GetAgeBars())
	require.Equal(t, int32(3), swap.GetAgeBars(), "永续从第 7 分钟起")
}

type unifiedSource struct{ replaySource }

func (unifiedSource) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"A"}, nil
}
