package replay

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/commonpb"

	"github.com/mooyang-code/moox/modules/strategy/internal/period"
	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
)

// “因子现算”来源（见 docs/模块/策略.md 第 10 节）：主帧不读 View，经主机网关调因子引擎的 EngineControl，按因子机本地 K 线
// 逐步现算。回放开始时 OpenReplay 建会话并给出可回放的各期，之后按期 StepReplay 取行，结束时 CloseReplay。附加来源与参考市场
// 仍读 View。主帧每个市场（每个因子任务）各开一个会话，同步推进。

const (
	// computeStepBytes 是单步响应的目标上限（引擎侧同样按 3 MiB 截断，服务包体上限是默认的 4 MiB）。
	computeStepBytes = 3 << 20
	// computeValueBytes 是估算单步大小时每个值的字节数：引擎每行除了本期的值还带上一期的同名列，列名与序列化开销都算在内。
	computeValueBytes = 48
	// computeCloseTimeout 是结束会话的超时：回放已经结束，关不上只影响引擎侧的内存，由引擎 6 小时闲置清理兜底。
	computeCloseTimeout = 30 * time.Second
	noteCompute         = "compute"
)

// errEngineSessionLost 是引擎上的回放会话不存在：引擎重启过（会话只在引擎内存里），回放不能接着算，要重新发起。
var errEngineSessionLost = errors.New("因子引擎重启，回放会话已失效，请重新发起")

// engineOfflineError 是经主机网关调因子引擎失败（转发失败、连不上、超时）：结果未知，回放失败，不重试。原始错误只给日志。
type engineOfflineError struct {
	method string
	raw    error
}

func (e *engineOfflineError) Error() string {
	return fmt.Sprintf("因子引擎不在线：经主机网关调用 %s 失败", e.method)
}

func (e *engineOfflineError) Unwrap() error { return e.raw }

// engineServiceError 是因子引擎返回的业务错误：引擎的说明是给用户看的中文（例如本地数据不覆盖区间、一期超过单步上限），原样带上。
type engineServiceError struct {
	method string
	code   commonpb.ErrorCode
	msg    string
}

func (e *engineServiceError) Error() string {
	return fmt.Sprintf("因子引擎 %s 失败：%s", e.method, e.msg)
}

// engineRetError 把引擎的返回码换成错误；StepReplay 的 NOT_FOUND 是会话已失效。
func engineRetError(method string, info *commonpb.RetInfo) error {
	if info == nil || info.GetCode() == commonpb.ErrorCode_SUCCESS {
		return nil
	}
	if method == "StepReplay" && info.GetCode() == commonpb.ErrorCode_NOT_FOUND {
		return errEngineSessionLost
	}
	return &engineServiceError{method: method, code: info.GetCode(), msg: info.GetMsg()}
}

// ComputeColumns 返回“因子现算”请求引擎返回的列：与 View 来源读主帧的列相同（close、当期列、bars[-1] 列，用到 params() 时
// 加 __params），计资金费时再加资金费列。
func ComputeColumns(program *dsl.Program, fundingColumn string) []string {
	columns := mainColumns(program)
	if fundingColumn != "" {
		columns = uniqueStrings(append(columns, fundingColumn))
	}
	return columns
}

// ComputeStepPeriods 返回“因子现算”每步请求的期数：按“序列数 × 列数 × 每个值约 48 字节”估算一期的大小，单步控制在 3 MiB 以内、
// 至少 1 期，再不超过 limit（与 View 来源的分段一致，取消与进度按段生效）；声明了 factor_params 时（tuned）固定 1 期：
// 下一期的参数要等这一期求值之后才有。一期就超过 3 MiB 时返回错误（提交时拒绝，提示减少引用的列）。
func ComputeStepPeriods(series, columns int, tuned bool, limit int) (int, error) {
	perPeriod := max(series, 1) * max(columns, 1) * computeValueBytes
	if perPeriod > computeStepBytes {
		return 0, fmt.Errorf("“因子现算”一期的因子帧约 %d 字节（%d 条序列 × %d 列），超过单步上限 %d 字节：减少策略引用的列", perPeriod, series, columns, computeStepBytes)
	}
	if tuned {
		return 1, nil
	}
	return max(1, min(computeStepBytes/perPeriod, max(limit, 1))), nil
}

// computeSource 是“因子现算”的输入来源：主帧每个市场（每个因子任务）在引擎上各开一个会话，同步推进。
type computeSource struct {
	runner   *Runner
	job      store.Replay
	resolved input.Resolved
	program  *dsl.Program
	// sessions 是主帧各市场的会话（现货在前），与 markets 一一对应。
	sessions []*computeSession
	// markets 是主帧的市场：只用到各附加来源的读取器与年龄判定的已通过集合，行与历史读取器为空（主帧的行来自引擎）。
	markets []*viewMarket
	// refs 是参考市场的读取器：只读 View 里年龄判定的历史，与 View 来源相同。
	refs  []*viewMarket
	abort func(context.Context) error
	// periods 是各会话给出的各期的交集（data_time 的 Unix 秒，升序）；bars 是对应的周期。
	periods []int64
	bars    []period.PeriodBoundaries
	// stepPeriods 是每步请求的期数（按全部会话的序列数合计估算）。
	stepPeriods int
	// applied 是最近一段各期实际生效的逐期提交参数（bar_start 的 Unix 秒 → 参数，各会话合并）；rejected 是同一段各期被引擎拒绝的参数的说明。
	applied  map[int64]engine.ParamSet
	rejected map[int64][]string
	// note 是写进局限说明的会话概况：被引擎排除的标的、预热不足的标的。
	note string
}

// computeSession 是一个市场（一个因子任务）在引擎上的回放会话。
type computeSession struct {
	market *viewMarket
	task   store.ReplayTask
	// id 是引擎上的会话 ID：只有一个会话时就是回放 ID，多个时加市场后缀（见 computeSessionID）。
	id     string
	opened bool
	// periods 是这个会话的 OpenReplay 给出的各期，index 是它们的下标。
	periods []int64
	index   map[int64]int
	// tail 是上一段最后一期（bar_start 为 tailAt）的行：策略用到 bars[-1] 时，下一段第一期的上一根读它。
	tail   []upstream.Row
	tailAt time.Time
}

// computeSessionID 返回一个市场在引擎上的会话 ID：只有一个主帧任务时就是回放 ID；多个任务时每个任务的会话 ID 不同（引擎按会话 ID
// 登记，同一个 ID 再开会顶掉前一个），加市场后缀。
func computeSessionID(replayID, market string, sessions int) string {
	if sessions <= 1 {
		return replayID
	}
	return replayID + "#" + market
}

// CloseInterruptedSessions 在进程启动、把遗留的运行中回放标成 interrupted 之前调用：逐个关掉“因子现算”回放留在因子引擎上的会话
// （进程在回放中途退出时没人关，会话占着整段快照的内存与准入预留，要等引擎 6 小时闲置清理）。尽力而为：关不上只记日志，
// 会话不存在时引擎返回 closed=false、不报错。回放登记了多个市场的任务时，加市场后缀的会话也逐个关（参考市场的任务没有会话，
// 多关一次无害）。返回读取运行中回放的错误。
func CloseInterruptedSessions(ctx context.Context, repo *store.Store, engine upstream.EngineClient, logf func(format string, args ...any)) error {
	if repo == nil || engine == nil {
		return nil
	}
	ids, err := repo.RunningReplayIDs(ctx, store.ReplayInputCompute)
	if err != nil {
		return err
	}
	for _, id := range ids {
		sessionIDs := []string{id}
		if job, err := repo.GetReplay(ctx, id); err == nil {
			var markets []string
			for key := range job.Tasks {
				if !strings.Contains(key, ":") {
					markets = append(markets, key)
				}
			}
			if len(markets) > 1 {
				sort.Strings(markets)
				for _, market := range markets {
					sessionIDs = append(sessionIDs, computeSessionID(id, market, len(markets)))
				}
			}
		}
		for _, sessionID := range sessionIDs {
			closeCtx, cancel := context.WithTimeout(ctx, computeCloseTimeout)
			rsp, err := engine.CloseReplay(closeCtx, &factorpb.CloseReplayReq{ReplayId: sessionID})
			cancel()
			if err == nil {
				err = engineRetError("CloseReplay", rsp.GetRetInfo())
			}
			if err != nil && logf != nil {
				logf("进程重启：结束回放 %s 留在因子引擎上的会话 %s 失败（由引擎闲置清理）：%v；原始错误：%v", id, sessionID, err, upstream.RawCause(err))
			}
		}
	}
	return nil
}

// newComputeSource 建立“因子现算”的会话：主帧每个市场按提交的区间（bars 的首尾）各调一次 OpenReplay，可回放的各期取各会话给出的交集；
// 任一会话建不起来时已建的都关掉。返回之后由调用方负责 Close。
func (r *Runner) newComputeSource(ctx context.Context, job store.Replay, resolved input.Resolved, program *dsl.Program, markets []*replayMarket, bars []period.PeriodBoundaries, abort func(context.Context) error) (*computeSource, error) {
	if r.Engine == nil {
		return nil, errors.New("因子引擎客户端未配置，不能用“因子现算”来源回放")
	}
	source := &computeSource{runner: r, job: job, resolved: resolved, program: program, abort: abort}
	for _, market := range markets {
		reader := newViewMarket(market, job.Tasks[market.market], nil, nil)
		source.markets = append(source.markets, reader)
		source.sessions = append(source.sessions, &computeSession{market: reader, task: job.Tasks[market.market], id: computeSessionID(job.ReplayID, market.market, len(markets))})
	}
	if err := attachSources(ctx, r.Client, job.SpaceID, resolved, program, source.markets, abort); err != nil {
		return nil, err
	}
	refs, err := referenceReaders(ctx, r.Client, job, resolved, abort)
	if err != nil {
		return nil, err
	}
	source.refs = refs
	columns := ComputeColumns(program, job.FundingColumn)
	extra := 0
	// 年龄由引擎按本地数据里序列的第一根算，与快照长度无关；预热只需多算上一根（bars[-1]）。
	if program.UsesPreviousBar {
		extra = 1
	}
	notes := make([]string, 0, len(source.sessions))
	for _, session := range source.sessions {
		rsp, err := r.Engine.OpenReplay(ctx, &factorpb.OpenReplayReq{
			ReplayId: session.id, TaskId: session.task.FactorTaskID, From: bars[0].StorageStart.UTC().Format(time.RFC3339),
			To: bars[len(bars)-1].BarEnd.UTC().Format(time.RFC3339), ExtraPeriods: int32(extra), Columns: columns,
		})
		if err != nil {
			// 结果未知：引擎可能已经登记了会话（例如响应在中转上丢了、或超时），尽力关一次；会话不存在时引擎返回 closed=false、不报错。
			session.opened = true
			_ = source.Close(ctx)
			return nil, &engineOfflineError{method: "OpenReplay", raw: err}
		}
		if err := engineRetError("OpenReplay", rsp.GetRetInfo()); err != nil {
			_ = source.Close(ctx)
			return nil, err
		}
		session.opened = true
		note, err := source.adopt(session, rsp)
		if err != nil {
			_ = source.Close(ctx)
			return nil, err
		}
		notes = append(notes, note)
	}
	if err := source.intersect(); err != nil {
		_ = source.Close(ctx)
		return nil, err
	}
	source.note = "因子现算：主帧的因子按因子机本地 K 线与回放中的参数重新计算，不读 View 的历史值；年龄按因子机本地数据的根数判断；附加来源与参考市场仍读 View；" + strings.Join(notes, "；")
	series := 0
	for _, market := range markets {
		series += len(market.subjects)
	}
	if source.stepPeriods, err = ComputeStepPeriods(series, len(columns), len(program.Params) > 0, r.chunkBars()); err != nil {
		_ = source.Close(ctx)
		return nil, err
	}
	return source, nil
}

// adopt 记下一个会话给出的各期并核对成员：引擎目录里任务成员的定义与发起回放时固化的不一致时拒绝（引擎的目录可能还没跟上）。
// 返回这个会话的概况说明（多个会话时带市场名）。
func (s *computeSource) adopt(session *computeSession, rsp *factorpb.OpenReplayRsp) (string, error) {
	for _, factor := range rsp.GetFactors() {
		pinned, ok := s.job.Factors[factor.GetMemberKey()]
		if ok && pinned != readiness.Contract(factor.GetDefinitionHash(), factor.GetKind()) {
			return "", fmt.Errorf("因子引擎里成员 %s 的定义（%s）与发起回放时（%s）不一致：引擎的任务目录可能还没更新，请稍后重新发起",
				factor.GetMemberKey(), readiness.Contract(factor.GetDefinitionHash(), factor.GetKind()), pinned)
		}
	}
	session.periods = rsp.GetPeriodTimes()
	if len(session.periods) == 0 {
		return "", fmt.Errorf("因子引擎没有给出%s可回放的周期", s.marketPrefix(session))
	}
	session.index = make(map[int64]int, len(session.periods))
	for i, at := range session.periods {
		bar, err := period.FromStorageStart(s.resolved.Calendar, s.resolved.Bar, time.Unix(at, 0).UTC())
		if err != nil {
			return "", fmt.Errorf("因子引擎给出的周期 %s：%w", time.Unix(at, 0).UTC().Format(time.RFC3339), err)
		}
		if bar.StorageStart.Unix() != at || (i > 0 && at <= session.periods[i-1]) {
			return "", fmt.Errorf("因子引擎给出的周期 %s 没有按 %s 对齐或不是升序", time.Unix(at, 0).UTC().Format(time.RFC3339), s.resolved.Bar)
		}
		session.index[at] = i
	}
	// 引擎的候选只取源数据集当前在册（active）的标的，名单里消失的标的（不论何时下架，包括回放区间之后才下架的）不是候选、也不在
	// excluded 里；候选里本地数据不覆盖回放最后一期的再被排除。所以按数据集登记的个数与实际参与的个数说明差额。
	coverage := fmt.Sprintf("%s数据集登记 %d 个标的，参与 %d 个：只取源数据集当前在册、且本地数据覆盖到回放最后一期的标的，已下架的（不论何时下架）整段不参与，截面因子按较小的全集算，结果有幸存者偏差",
		s.marketPrefix(session), len(session.market.subjects), len(rsp.GetSubjects()))
	if excluded := rsp.GetExcluded(); len(excluded) > 0 {
		samples := make([]string, 0, 3)
		for _, item := range excluded[:min(3, len(excluded))] {
			samples = append(samples, item.GetSubjectId()+"（"+item.GetReason()+"）")
		}
		coverage += fmt.Sprintf("（在册但本地数据不覆盖的 %d 个，例如 %s）", len(excluded), strings.Join(samples, "、"))
	}
	parts := []string{coverage}
	if short := rsp.GetShortWarmup(); short > 0 {
		parts = append(parts, fmt.Sprintf("%d 个标的的本地数据晚于预热起点，因子前段为空值（例如 %s）", short, strings.Join(rsp.GetShortSamples(), "、")))
	}
	return strings.Join(parts, "；"), nil
}

// intersect 取各会话给出的各期的交集作为回放的各期：交集的区间内各会话的期次必须完全相同（两个市场的本地数据在区间中间缺的期不同，
// 同步推进就对不上，直接失败），区间外只有一个会话有的期不回放。
func (s *computeSource) intersect() error {
	from, to := s.sessions[0].periods[0], s.sessions[0].periods[len(s.sessions[0].periods)-1]
	for _, session := range s.sessions[1:] {
		from, to = max(from, session.periods[0]), min(to, session.periods[len(session.periods)-1])
	}
	if from > to {
		return errors.New("因子引擎给出的各市场可回放的周期没有重叠，无法同步回放")
	}
	var common []int64
	for i, session := range s.sessions {
		var inRange []int64
		for _, at := range session.periods {
			if at >= from && at <= to {
				inRange = append(inRange, at)
			}
		}
		if i == 0 {
			common = inRange
			continue
		}
		if !slices.Equal(common, inRange) {
			return fmt.Errorf("因子引擎给出的%s与%s可回放的周期在 %s 至 %s 之间不一致（本地数据缺的期不同），无法同步回放：先补齐因子机本地的历史再发起",
				marketLabel(s.sessions[0].market.market), marketLabel(session.market.market), time.Unix(from, 0).UTC().Format(time.RFC3339), time.Unix(to, 0).UTC().Format(time.RFC3339))
		}
	}
	s.periods = common
	s.bars = make([]period.PeriodBoundaries, 0, len(common))
	for _, at := range common {
		bar, err := period.FromStorageStart(s.resolved.Calendar, s.resolved.Bar, time.Unix(at, 0).UTC())
		if err != nil {
			return err
		}
		s.bars = append(s.bars, bar)
	}
	return nil
}

// marketPrefix 在多个会话时给说明加上市场名（“现货”“永续”），只有一个会话时为空。
func (s *computeSource) marketPrefix(session *computeSession) string {
	if len(s.sessions) <= 1 {
		return ""
	}
	return marketLabel(session.market.market)
}

// marketLabel 是市场的中文名。
func marketLabel(market string) string {
	switch market {
	case dsl.MarketSpot:
		return "现货"
	case dsl.MarketSwap:
		return "永续"
	}
	return market
}

// references 返回参考市场（只参与 age_markets: all 的年龄判定）。
func (s *computeSource) references() []*replayMarket {
	refs := make([]*replayMarket, 0, len(s.refs))
	for _, ref := range s.refs {
		refs = append(refs, ref.replayMarket)
	}
	return refs
}

// End 是最后一期之后：各期在会话期间不变。
func (s *computeSource) End() time.Time {
	return s.bars[len(s.bars)-1].StorageStart.Add(time.Nanosecond)
}

func (s *computeSource) Applied(key int64) engine.ParamSet { return s.applied[key] }

func (s *computeSource) Rejected(key int64) []string { return s.rejected[key] }

// Close 结束引擎上的全部会话。回放可能因取消或失败结束（ctx 也许已结束），所以用不随它取消的上下文另设超时；关不上只记日志，
// 返回第一个错误。
func (s *computeSource) Close(ctx context.Context) error {
	var first error
	for _, session := range s.sessions {
		if !session.opened {
			continue
		}
		session.opened = false
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), computeCloseTimeout)
		rsp, err := s.runner.Engine.CloseReplay(closeCtx, &factorpb.CloseReplayReq{ReplayId: session.id})
		cancel()
		if err == nil {
			err = engineRetError("CloseReplay", rsp.GetRetInfo())
		}
		if err != nil {
			s.runner.logf("回放 %s：结束因子引擎上的会话 %s 失败（由引擎闲置清理）：%v；原始错误：%v", s.job.ReplayID, session.id, err, upstream.RawCause(err))
			if first == nil {
				first = err
			}
		}
	}
	return first
}

// Load 取一段连续的期：每个会话按每步期数逐步请求，返回不足请求的期数（单步响应放不下）时从下一期接着请求，不请求本段之外的期。
// nextParams 是上一根策略求出的参数，只交给这一段的第一期（每个会话都给同一份），引擎之后各期沿用。任一会话失效（引擎重启）都整体失败。
func (s *computeSource) Load(ctx context.Context, segment []period.PeriodBoundaries, nextParams engine.ParamSet) (map[string]marketSegment, error) {
	s.applied = make(map[int64]engine.ParamSet, len(segment))
	s.rejected = make(map[int64][]string)
	loaded := make(map[string]marketSegment, len(s.markets)+len(s.refs))
	applied := make([]map[int64]engine.ParamSet, 0, len(s.sessions))
	for _, session := range s.sessions {
		rows, ages, sessionApplied, err := s.loadSession(ctx, session, segment, nextParams)
		if err != nil {
			return nil, err
		}
		applied = append(applied, sessionApplied)
		sources, err := loadSourceSegment(ctx, s.resolved, session.market, segment)
		if err != nil {
			return nil, err
		}
		loaded[session.market.market] = marketSegment{rows: input.GroupRows(rows), ages: &engineAges{ages: ages, passed: session.market.passed}, sources: sources}
	}
	s.mergeApplied(segment, applied)
	for _, ref := range s.refs {
		refAges, err := agePresence(ctx, ref.history, s.resolved, segment, ref.passed)
		if err != nil {
			return nil, fmt.Errorf("参考市场 %s：%w", ref.market, err)
		}
		loaded[ref.market] = marketSegment{ages: refAges}
	}
	return loaded, nil
}

// loadSession 在一个会话上取一段的行、年龄根数（bar_start → 标的 → 根数）与各期生效的参数（bar_start → 参数），被拒的参数并进 rejected。
func (s *computeSource) loadSession(ctx context.Context, session *computeSession, segment []period.PeriodBoundaries, nextParams engine.ParamSet) ([]upstream.Row, map[int64]map[string]int, map[int64]engine.ParamSet, error) {
	first, ok := session.index[segment[0].StorageStart.Unix()]
	if !ok || first+len(segment) > len(session.periods) {
		return nil, nil, nil, fmt.Errorf("周期 %s 不在因子引擎给出的%s回放区间内", segment[0].StorageStart.UTC().Format(time.RFC3339), s.marketPrefix(session))
	}
	want := first + len(segment)
	var rows []upstream.Row
	// 上一根不在本段里：用上一段最后一期的行（同一会话里算出的原样的行）；第一段没有上一段，用引擎给的上一期同名列补出。
	usesTail := s.program.UsesPreviousBar && session.tail != nil && session.tailAt.Equal(segment[0].PreviousStart)
	if usesTail {
		rows = append(rows, session.tail...)
	}
	var last []upstream.Row
	ages := make(map[int64]map[string]int, len(segment))
	applied := make(map[int64]engine.ParamSet, len(segment))
	params := paramsToPB(nextParams)
	for cursor := first; cursor < want; {
		if err := s.abort(ctx); err != nil {
			return nil, nil, nil, err
		}
		rsp, err := s.runner.Engine.StepReplay(ctx, &factorpb.StepReplayReq{ReplayId: session.id, PeriodTime: session.periods[cursor], Periods: int32(min(s.stepPeriods, want-cursor)), Params: params})
		if err != nil {
			return nil, nil, nil, &engineOfflineError{method: "StepReplay", raw: err}
		}
		if err := engineRetError("StepReplay", rsp.GetRetInfo()); err != nil {
			return nil, nil, nil, err
		}
		periods := rsp.GetPeriods()
		if len(periods) == 0 {
			return nil, nil, nil, fmt.Errorf("因子引擎从 %s 起没有返回%s任何一期", time.Unix(session.periods[cursor], 0).UTC().Format(time.RFC3339), s.marketPrefix(session))
		}
		for i, period := range periods {
			if cursor+i >= want || period.GetPeriodTime() != session.periods[cursor+i] {
				return nil, nil, nil, fmt.Errorf("因子引擎返回的第 %d 期（%s）与请求的期次不连续", i+1, time.Unix(period.GetPeriodTime(), 0).UTC().Format(time.RFC3339))
			}
			at := time.Unix(period.GetPeriodTime(), 0).UTC()
			synthesize := cursor+i == first && s.program.UsesPreviousBar && !usesTail && !segment[0].PreviousStart.IsZero()
			last = last[:0:0]
			for _, row := range period.GetRows() {
				current := upstream.Row{SubjectID: row.GetSubjectId(), SeriesTag: row.GetSeriesTag(), DataTime: at, Values: row.GetValues(), Params: rowParams(row.GetParams())}
				rows, last = append(rows, current), append(last, current)
				if synthesize && len(row.GetPrevious()) > 0 {
					rows = append(rows, upstream.Row{SubjectID: row.GetSubjectId(), SeriesTag: row.GetSeriesTag(), DataTime: segment[0].PreviousStart, Values: row.GetPrevious()})
				}
				recordAge(ages, at.Unix(), row)
			}
			applied[at.Unix()] = appliedParams(period.GetAppliedParams())
			for _, rejection := range period.GetRejectedParams() {
				s.rejected[at.Unix()] = append(s.rejected[at.Unix()], fmt.Sprintf("因子引擎拒绝了%s成员 %s 这一期的参数（%s），沿用上一期生效的参数", s.marketPrefix(session), rejection.GetMemberKey(), rejection.GetReason()))
			}
		}
		cursor += len(periods)
		params = nil // 请求的参数只给这一段的第一期，之后引擎沿用
	}
	if s.program.UsesPreviousBar {
		session.tail, session.tailAt = last, segment[len(segment)-1].StorageStart
	}
	return rows, ages, applied, nil
}

// mergeApplied 把各会话这一段各期生效的参数并成每期一份记录：各会话收到同一份请求，生效的参数通常相同，记录取第一个会话
// （现货在前）的；某个会话这一期生效的与它不同（例如一边拒绝了参数、沿用上一期，含一边有、一边没有）时写进这一期的说明，不静默丢掉。
func (s *computeSource) mergeApplied(segment []period.PeriodBoundaries, applied []map[int64]engine.ParamSet) {
	for _, bar := range segment {
		at := bar.StorageStart.Unix()
		first := applied[0][at]
		if first != nil {
			s.applied[at] = first
		}
		for i := 1; i < len(applied); i++ {
			if other := applied[i][at]; !reflect.DeepEqual(first, other) {
				s.rejected[at] = append(s.rejected[at], fmt.Sprintf("%s生效的参数与%s不同（%s：%v），记录里取%s的", marketLabel(s.sessions[i].market.market),
					marketLabel(s.sessions[0].market.market), marketLabel(s.sessions[i].market.market), other, marketLabel(s.sessions[0].market.market)))
			}
		}
	}
}

// recordAge 记下一行的年龄根数（bar_start → 标的 → 根数）：同一标的有多个序列时取最大的（这一根本来就是歧义，不参与求值）。
func recordAge(ages map[int64]map[string]int, at int64, row *factorpb.ReplayRow) {
	if ages[at] == nil {
		ages[at] = map[string]int{}
	}
	if age := int(row.GetAgeBars()); age > ages[at][row.GetSubjectId()] {
		ages[at][row.GetSubjectId()] = age
	}
}

// engineAges 按引擎给的根数判定年龄：本根有行、且这条序列截至本根的本地根数不少于 n 即通过。与 View 来源一样，通过后记入
// 已通过集合（按 n 分开，整次回放共用），之后本根没有行也不再被剔除。
type engineAges struct {
	ages   map[int64]map[string]int
	passed map[int]map[string]struct{}
}

func (a *engineAges) probe(n int, bar period.PeriodBoundaries) input.AgeProbe {
	return func(_ context.Context, instruments []string) (map[string]struct{}, error) {
		if a.passed[n] == nil {
			a.passed[n] = make(map[string]struct{})
		}
		passed := a.passed[n]
		current := a.ages[bar.StorageStart.Unix()]
		satisfied := make(map[string]struct{}, len(instruments))
		for _, id := range instruments {
			if _, ok := passed[id]; ok {
				satisfied[id] = struct{}{}
				continue
			}
			if age, ok := current[id]; ok && age >= n {
				satisfied[id] = struct{}{}
				passed[id] = struct{}{}
			}
		}
		return satisfied, nil
	}
}

// paramsToPB 把策略求出的下一期参数换成请求里的成员参数（按成员键排序）。
func paramsToPB(params engine.ParamSet) []*factorpb.MemberParams {
	if len(params) == 0 {
		return nil
	}
	members := make([]string, 0, len(params))
	for member := range params {
		members = append(members, member)
	}
	sort.Strings(members)
	out := make([]*factorpb.MemberParams, 0, len(members))
	for _, member := range members {
		item := &factorpb.MemberParams{MemberKey: member, Params: make(map[string]*factorpb.ParamValue, len(params[member]))}
		for name, value := range params[member] {
			item.Params[name] = &factorpb.ParamValue{DefaultValue: value.Default, Overrides: value.Overrides}
		}
		out = append(out, item)
	}
	return out
}

// appliedParams 把引擎返回的本期生效参数换成参数快照；没有时返回 nil（成员按自己的参数计算）。
func appliedParams(members []*factorpb.MemberParams) engine.ParamSet {
	if len(members) == 0 {
		return nil
	}
	out := make(engine.ParamSet, len(members))
	for _, member := range members {
		values := make(map[string]engine.ParamValue, len(member.GetParams()))
		for name, value := range member.GetParams() {
			values[name] = engine.ParamValue{Default: value.GetDefaultValue(), Overrides: value.GetOverrides()}
		}
		out[member.GetMemberKey()] = values
	}
	return out
}

// rowParams 把一行各成员实际用的参数换成 params() 读取的格式（成员键 → 参数名 → 值）。
func rowParams(params map[string]*factorpb.ReplayParamValues) map[string]map[string]float64 {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]map[string]float64, len(params))
	for member, values := range params {
		out[member] = values.GetValues()
	}
	return out
}
