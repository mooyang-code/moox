package localdata

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/seriessync"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"trpc.group/trpc-go/trpc-go/log"
)

// Prepare 为一个任务准备数据：读数据集字段 → 取同步基线或跟进变更 → 把本地补到最新已收盘的一根 → 载入内存窗口。
// 可以重复调用（目录变化、任务新启用时）；窗口载入完成之前该任务的实时周期按预热处理（本期不算、记账本）。
// 窗口内存会超出进程预算时拒绝，并给出原因。
func (d *Data) Prepare(ctx context.Context, set domain.EngineTask, subjects []string) error {
	src := sourceOf(set.Set)
	step, err := seriesstore.Step(set.Set.Freq)
	if err != nil {
		return err
	}
	columns, err := d.cfg.Storage.ListColumns(ctx, src.SpaceID, src.DatasetID)
	if err != nil {
		return fmt.Errorf("读取源数据集字段：%w", err)
	}
	fields := seriessync.FieldsFromColumns(columns)
	if len(fields) == 0 {
		return fmt.Errorf("源数据集 %s/%s 没有可用的字段", src.SpaceID, src.DatasetID)
	}
	setLookback := d.lookbackFor(src, set.Members)
	// 回看超限只拒绝这个任务：在登记之前检查，并撤掉它原有的登记（定义改长的情况），不动数据源共用的就绪状态。
	if err := d.LookbackError(set); err != nil {
		d.ReleaseTask(set.Set)
		return err
	}
	numeric := serieswindow.NumericFields(fields)

	var st *source
	for {
		d.mu.Lock()
		st = d.sources[src]
		if st == nil {
			st = &source{regs: map[string]setReg{}}
			d.sources[src] = st
		}
		d.mu.Unlock()

		st.mu.Lock()
		if st.dropped {
			st.mu.Unlock()
			continue
		}
		break
	}
	defer st.mu.Unlock()
	d.settleFlush(st)
	// 登记本任务，再按全部登记任务的标的并集与最大回看准备数据源。
	st.regsMu.Lock()
	st.regs[set.Set.TaskID] = setReg{lookback: setLookback, subjects: append([]string(nil), subjects...), observed: observedMembers(set.Members)}
	lookback, union, observed := 1, map[string]struct{}{}, false
	for _, reg := range st.regs {
		lookback = max(lookback, reg.lookback)
		observed = observed || reg.observed
		for _, subject := range reg.subjects {
			union[subject] = struct{}{}
		}
	}
	st.regsMu.Unlock()
	subjects = make([]string, 0, len(union))
	for subject := range union {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	previous := make(map[string]struct{}, len(st.subjects))
	for _, subject := range st.subjects {
		previous[subject] = struct{}{}
	}
	st.spec = seriessync.Spec{Source: src, Fields: fields, Lookback: lookback}
	// 标的范围：任务列出的标的，或「全部标的」任务下数据集里状态为 active 的标的。被移出的标的（任务里去掉，或在数据集的标签里
	// 删除）不再跟进变更（设计 6.2）；清单变化时预载会重新准备并刷新这个范围。
	st.spec.Subjects = make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		st.spec.Subjects[subject] = struct{}{}
	}
	st.types = seriessync.ValueTypes(columns)
	st.numeric = numeric
	st.subjects = append([]string(nil), subjects...)
	st.covered = nil

	// 数据集保留期决定 N 能不能从 Storage 补齐，也决定新上市豁免是否成立。
	info, err := d.cfg.Storage.GetDataset(ctx, src.SpaceID, src.DatasetID)
	if err != nil {
		return fmt.Errorf("读取源数据集信息：%w", err)
	}
	st.exemptNew = d.newListingExempt(src, step, lookback, info.Retention)

	// 有 observed 任务时，窗口还要放进本地存储里回看窗口内有数据、但不在任何任务标的范围里的标的（例如已下架、移出全集的标的）。
	// 它们只读本地历史，不同步、不跟进源数据的修正。
	var history []string
	if observed {
		now, err := d.axisNow(src)
		if err != nil {
			return err
		}
		// 窗口载入回看区间内、不在登记标的并集里的全部本地标的，不按“正在同步”过滤：过滤放到计算时（HistorySubjects）做。
		// 否则刚下架的标的会因为过时的“正在同步”清单被移出窗口，之后实时结果一直少它。
		if history, err = HistoryCandidates(d.cfg.Store, src, now.Add(-time.Duration(lookback)*step), now,
			HistoryRule{Exclude: subjects, Synced: subjects}); err != nil {
			return err
		}
	}
	// 窗口内存超出上限或进程预算：拒绝启用并给出明确原因，不让窗口把计算挤出内存。
	need := serieswindow.BytesPerSeries(lookback, d.cfg.Headroom, len(numeric)) * int64(max(len(subjects)+len(history), 1))
	if d.cfg.WindowMax > 0 {
		if total := d.windowBytesExcept(src) + need; total > d.cfg.WindowMax {
			st.ready.Store(false)
			st.blocked = fmt.Errorf("窗口内存估算 %d 字节，加上其他数据源共 %d 字节，超过 window.max_memory（%d 字节）", need, total, d.cfg.WindowMax)
			return st.blocked
		}
	}
	if d.cfg.Admit != nil && !d.cfg.Admit.CanResident("window:"+src.String(), need) {
		st.ready.Store(false)
		st.blocked = fmt.Errorf("窗口内存估算 %d 字节，加上其他常驻内存超过进程内存预算 memory.total_budget", need)
		return st.blocked
	}

	// 基线必须在读数据之前取：之后从这个位点继续读变更，取位点与读数据之间的写入不会被漏掉。
	_, hasCursor, err := d.syncer.Cursor(src)
	if err != nil {
		return err
	}
	if !hasCursor {
		if _, err := d.syncer.Baseline(ctx, src); err != nil {
			return fmt.Errorf("取变更日志基线：%w", err)
		}
	} else {
		follow, err := d.syncer.Follow(ctx, st.spec)
		if err != nil {
			return fmt.Errorf("跟进变更日志：%w", err)
		}
		if follow.Gap {
			st.ready.Store(false)
			st.blocked = ErrChangeGap
			return ErrChangeGap
		}
	}
	st.blocked = nil

	// 本地补到最新已收盘的一根：停机缺口、新标的的回看窗口一次性拉齐。
	end, err := d.axisNow(src)
	if err != nil {
		return err
	}
	pull, err := d.syncer.Pull(ctx, st.spec, subjects, end)
	if err != nil {
		return fmt.Errorf("同步本地数据：%w", err)
	}
	if len(pull.Failed) > 0 && len(pull.Failed) == len(subjects) {
		return fmt.Errorf("%w：所有标的同步失败", storageio.ErrInfra)
	}
	// 重新加入任务的标的：移出期间 Storage 里的修正没有被跟进，先把本地已有的历史与 Storage 对账（只在同一进程里见过它被移出时）。
	if len(previous) > 0 {
		var readded []string
		for _, subject := range subjects {
			if _, was := previous[subject]; !was {
				readded = append(readded, subject)
			}
		}
		if len(readded) > 0 {
			if _, err := d.syncer.Resync(ctx, st.spec, readded); err != nil {
				return fmt.Errorf("重新对账重新加入任务的标的：%w", err)
			}
		}
	}
	// 本地不足 N 根的标的从 Storage 往前补（保留期内有多少补多少，决定 24）；已经够的只是一次本地比对。
	if short, err := d.shortOfLookback(st, subjects, lookback); err != nil {
		return err
	} else if len(short) > 0 {
		if _, err := d.syncer.Backfill(ctx, st.spec, short, lookback, nil); err != nil {
			return fmt.Errorf("补本地历史到 %d 根：%w", lookback, err)
		}
	}

	window := st.window
	if window == nil || window.Lookback() != lookback || !sameStrings(window.Fields(), numeric) {
		window, err = serieswindow.New(serieswindow.Config{Source: src, Lookback: lookback, Headroom: d.cfg.Headroom}, int64(step/time.Second), numeric)
		if err != nil {
			return err
		}
		// 读租约只覆盖单次计算：持有超过一个计算周期说明有人长期占着旧一代数组（设计 4.1）。
		window.SetLeaseWarn(step, func(held time.Duration) {
			log.Warnf("factor_window_lease_held_long source=%s held=%s", src, held.Round(time.Millisecond))
		})
	}
	local, err := d.cfg.Store.Subjects(src)
	if err != nil {
		return err
	}
	// 本地和 Storage 都没有任何数据的标的：拉取成功返回了空结果（拉取失败的不算，那只是同步失败）。
	// 全部标的都没有数据时数据源未就绪，要导入或移除；只有一部分没有时（停牌超过保留期、刚加入还没采到）不能让它拖住整个任务：
	// 记警告，它们不进内存窗口，每期按本期不可用处理；实时周期照常按回看范围拉取它们，拉到数据的那一期就载入窗口。
	// 任务检查的“没有数据的标的”一项现算列出它们。2026-11-24 停牌两个多月的 601059.XSHG 的最后一批 5m 超出保留期，
	// 沪深 300 的因子任务因此停算了一上午。
	if noData := noDataSubjects(subjects, local, pull.Failed); len(noData) > 0 {
		if len(noData) == len(subjects) {
			st.ready.Store(false)
			st.blocked = &NoDataError{Subjects: noData}
			return st.blocked
		}
		log.Warnf("factor_subjects_without_data source=%s %v；这些标的本期不可用，其余照常计算", src, &NoDataError{Subjects: noData})
	}
	// 窗口按全部登记任务需要的标的并集载入与保留：任务的标的，加上 observed 任务的历史截面标的。
	want := append(intersect(subjects, local), history...)
	if err := window.Load(ctx, d.cfg.Store, want); err != nil {
		return fmt.Errorf("载入内存窗口：%w", err)
	}
	if removed := window.Retain(want); removed > 0 {
		log.Infof("factor_window_subjects_released source=%s removed=%d", src, removed)
	}
	st.window = window
	st.win.Store(window)
	st.ready.Store(true)
	if d.cfg.Admit != nil {
		d.cfg.Admit.SetResident("window:"+src.String(), window.Stats().Bytes)
	}
	return nil
}

// observedMembers 报告任务里是否有成员写了 cross_section_history: observed。
func observedMembers(members []domain.MemberDef) bool {
	for _, member := range members {
		if member.Factor.ObservedHistory() {
			return true
		}
	}
	return false
}

// HistoryRule 决定 observed 成员历史截面额外纳入哪些标的（设计 F8“在成员标的范围内”）：本地存储里数据与读取区间有交集、
// 不在 Exclude（本期可用、上游失败）里，并且属于下面两类之一：
//   - 本任务范围（Own）里本期不可用的标的；
//   - 已经没有任何登记任务在同步的标的（Synced 之外：已下架、已移出全集）。
//
// 其他任务正在同步、而本任务范围不含的标的不纳入：它们本期的数据到没到取决于那个任务先跑还是后跑，纳入会让结果随执行顺序变化。
// 按本地存储的内容与登记取，不按当前全集名单取，所以重启前后结果一致。实时、补算、回放都按这条规则。
type HistoryRule struct {
	// Exclude 是不纳入的标的（本期可用、上游失败）；Synced 是正在同步的标的。
	Exclude, Synced []string
	// Own 是本任务范围：其中本期不可用、又不在 Exclude 里的标的即使正在同步也纳入。实时与补算的 Exclude 已经包含计划的全部
	// 期望标的（本期可用与上游失败），所以 Own 只在回放中起作用：回放的候选标的里被排除在回放标的之外的（例如数据到不了回放终点）。
	Own []string
}

// HistoryCandidates 返回按 rule 应纳入的标的（按标的排序）。每次调用扫描一遍本地标的边界；只在有 observed 成员时调用。
func HistoryCandidates(store *seriesstore.Store, src seriesstore.Source, from, to time.Time, rule HistoryRule) ([]string, error) {
	set := func(list []string) map[string]struct{} {
		out := make(map[string]struct{}, len(list))
		for _, subject := range list {
			out[subject] = struct{}{}
		}
		return out
	}
	exclude, own, synced := set(rule.Exclude), set(rule.Own), set(rule.Synced)
	bounds, err := store.SubjectBounds(src)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for subject, b := range bounds {
		if _, skip := exclude[subject]; skip {
			continue
		}
		_, mine := own[subject]
		_, others := synced[subject]
		if !mine && others {
			continue
		}
		if !b.Latest.Before(from) && b.Earliest.Before(to) {
			out = append(out, subject)
		}
	}
	sort.Strings(out)
	return out, nil
}

// syncedSubjects 返回数据源上“正在同步”的标的（Config.Synced：预载最近一次取到的在册清单），与运行时的登记、准备状态无关，
// 所以重启时任务逐个就绪、某个任务准备失败都不改变它。没有配置、或预载还没取到过清单时为空。
func (d *Data) syncedSubjects(ctx context.Context, src seriesstore.Source) ([]string, error) {
	if d.cfg.Synced == nil {
		return nil, nil
	}
	return d.cfg.Synced(ctx, src)
}

// HistorySubjects 实现 pipeline.HistorySource：读取区间 [ReadStart, TargetEnd) 内按 HistoryRule 纳入的标的。
// 本任务范围是计划的期望全集；正在同步的标的是预载的在册清单，加上本任务范围。只在有 observed 成员时调用，每次扫描一遍本地边界。
func (d *Data) HistorySubjects(ctx context.Context, plan pipeline.Plan) ([]string, error) {
	from, err := pipeline.ReadStart(periodclock.Continuous{}, plan)
	if err != nil {
		return nil, err
	}
	src := sourceOf(plan.Set)
	synced, err := d.syncedSubjects(ctx, src)
	if err != nil {
		return nil, fmt.Errorf("列出数据源上正在同步的标的：%w", err)
	}
	return HistoryCandidates(d.cfg.Store, src, from, plan.TargetEnd, HistoryRule{
		Exclude: append(append([]string(nil), plan.Available...), plan.UpstreamFailed...),
		Own:     plan.Expected, Synced: append(append([]string(nil), synced...), plan.Expected...),
	})
}

// withHistory 返回 subjects 加上 history（observed 成员历史截面的标的）。
func withHistory(subjects, history []string) []string {
	if len(history) == 0 {
		return subjects
	}
	return append(append([]string(nil), subjects...), history...)
}

// factorsOverLookback 返回回看超过 N 的成员（没有则为 nil）。
func factorsOverLookback(members []domain.MemberDef, n int) *LookbackExceededError {
	var names []string
	most := 0
	for _, member := range members {
		if member.Factor.LookbackPeriods > n {
			names = append(names, member.Key)
			most = max(most, member.Factor.LookbackPeriods)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return &LookbackExceededError{N: n, Factors: names, Max: most}
}

// LookbackError 报告任务里有没有因子的回看超过数据源的回看数量 N（设计 8.1）。只看这个任务：
// 同一数据源上的其他任务照常计算，引擎按任务把它排除出追赶并给出“回看数量”检查项。
func (d *Data) LookbackError(set domain.EngineTask) error {
	if over := factorsOverLookback(set.Members, d.lookbackFor(sourceOf(set.Set), set.Members)); over != nil {
		return over
	}
	return nil
}

// newListingExempt 判断新上市豁免对数据源是否成立（设计 6.2）：N 不超过数据集保留期能容纳的根数。
// 超过时 Storage 的首根可能是被保留期截断的，不能据此认定是新上市。容量按轴上的根数计算：
// 7×24 市场就是保留期 ÷ 周期，有交易时段的市场只数保留期内的交易时段。
func (d *Data) newListingExempt(src seriesstore.Source, step time.Duration, lookback int, retention string) bool {
	period, err := storagepolicy.ParsePeriod(retention)
	if err != nil {
		return false
	}
	if period.Forever {
		return true
	}
	axis, err := periodclock.AxisFor(src.SpaceID, src.Freq)
	if err != nil {
		return false
	}
	now := d.cfg.Now()
	end, err := axis.AxisEnd(now)
	if err != nil {
		return false
	}
	start, err := axis.AxisEnd(now.Add(-period.Duration))
	if err != nil {
		return false
	}
	return int64(lookback) <= int64(end.Sub(start)/step)
}

// shortOfLookback 返回本地已有数据但不足 lookback 根的标的。
func (d *Data) shortOfLookback(st *source, subjects []string, lookback int) ([]string, error) {
	step, err := seriesstore.Step(st.spec.Source.Freq)
	if err != nil {
		return nil, err
	}
	var short []string
	for _, subject := range subjects {
		b, ok, err := d.cfg.Store.Bounds(st.spec.Source, subject)
		if err != nil {
			return nil, err
		}
		if ok && int(b.Latest.Sub(b.Earliest)/step)+1 < lookback {
			short = append(short, subject)
		}
	}
	return short, nil
}

// noDataSubjects 是任务里本地没有数据、同步又没有失败的标的：Storage 成功返回了空结果。
func noDataSubjects(subjects, local, failed []string) []string {
	have := make(map[string]struct{}, len(local)+len(failed))
	for _, s := range local {
		have[s] = struct{}{}
	}
	for _, s := range failed {
		have[s] = struct{}{}
	}
	var out []string
	for _, s := range subjects {
		if _, ok := have[s]; !ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
