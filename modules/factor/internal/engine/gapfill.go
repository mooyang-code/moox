package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/gapledger"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/seriesset"
	"trpc.group/trpc-go/trpc-go/log"
)

// 跳过恢复：引擎每隔一段时间扫描账本里待恢复的 (标的, 周期, 因子)，在进程内用补算执行器做「只填充」重算：
// 窗口检查通过才计算并写入，仍有洞就什么都不写（该键最初跳过时已写成 NULL）。写入确认后追加 repaired。
// 待办有两种来源：回看窗口有空洞，或者实时周期里标的还在预热（reason=warming，本期没有算）。

type setByID interface {
	SetByID(taskID string) (domain.EngineTask, SetLookup)
}

type chunkWithRunner interface {
	RunChunkWith(ctx context.Context, source recalcexec.SelectionSource, start, end time.Time, after func(recalcexec.Selection, pipeline.Outcome, error)) (pipeline.Outcome, error)
}

type universeReader interface {
	ComputedUniverse(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (storageio.Universe, bool, error)
}

type gapFiller struct {
	ledger   *gapledger.Ledger
	sets     setByID
	source   SourceDataset
	exec     chunkWithRunner
	universe universeReader
	// unified 判断任务的源数据集是不是统一数据集（market_type=multi），为 nil 表示都不是：只有统一数据集的时序恢复才逐期读冻结的序列全集。
	unified func(ctx context.Context, set domain.FactorTask) (bool, error)
	clock   periodclock.Clock
	active  func() bool
	cfg     GapConfig
	now     func() time.Time
	// onGaveUp 在条目被记为 gave_up 时调用（指标用，可为 nil）。
	onGaveUp func(entries int)
	// onRepaired 在一批待办确认补齐后按周期调用（可为 nil），在任务锁内。引擎据此把补齐的最新一期记入运行摘要。
	onRepaired func(taskID string, period RepairedPeriod)
	// kick 让恢复循环立刻做一轮（预热完成、记入预热中跳过的项时），为 nil 时只按 SweepInterval 轮询。
	kick <-chan struct{}
	// warmup 返回各任务的预热状态（可为 nil）：已就绪任务里因预热中跳过的待办不再等 Settle。
	warmup func() []pipeline.WarmupStatus
	// filled 判断某任务某一期是否已收到补齐通知（可为 nil）。
	filled func(taskID string, period time.Time) bool
	// lastTried 记录每个组最近一次尝试的时间：一轮只处理 MaxGroupsPerSweep 个组，按「最久没试过的优先」轮转，
	// 前面一直补不上的组不会让后面已经补齐的组长期得不到执行机会。
	lastTried map[string]time.Time
	// universeMemo 是一轮恢复里已读到的周期 marker 冻结全集，按（结果数据集、触发事件、周期）缓存：同一周期的 marker 一轮只读一次，
	// 每轮开始时清空（读失败的不缓存）。
	universeMemo map[universeKey]universeMemoEntry
}

type universeKey struct {
	dataset, trigger string
	period           int64
}

type universeMemoEntry struct {
	universe storageio.Universe
	found    bool
}

// computedUniverse 读周期 marker 里冻结的全集，一轮内同一（数据集、触发事件、周期）只读一次。
func (g *gapFiller) computedUniverse(ctx context.Context, set domain.FactorTask, trigger string, period int64) (storageio.Universe, bool, error) {
	key := universeKey{dataset: set.ResultDatasetID, trigger: trigger, period: period}
	if cached, ok := g.universeMemo[key]; ok {
		return cached.universe, cached.found, nil
	}
	universe, found, err := g.universe.ComputedUniverse(ctx, set.SpaceID, set.ResultDatasetID, trigger, period)
	if err != nil {
		return storageio.Universe{}, false, err
	}
	if g.universeMemo == nil {
		g.universeMemo = make(map[universeKey]universeMemoEntry)
	}
	g.universeMemo[key] = universeMemoEntry{universe: universe, found: found}
	return universe, found, nil
}

// markerMayBeLate 报告“读不到 marker”是否可能只是还没写：待办是实时周期跳过时记下的，marker 在这一期结果写完之后才写，
// 恢复可能抢在它前面。待办刚记下不足 Settle 时保留待办、下一轮再试，不退回兜底推导，也不放弃。
func (g *gapFiller) markerMayBeLate(firstSeen time.Time) bool {
	if g.now == nil || g.cfg.Settle <= 0 {
		return false
	}
	return g.now().UTC().Sub(firstSeen) < g.cfg.Settle
}

// Run 按 SweepInterval 轮询、或者被唤醒（预热完成、记入预热中跳过的项），直到 ctx 结束。
func (g *gapFiller) Run(ctx context.Context) {
	ticker := time.NewTicker(g.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-g.kick:
		}
		g.tick(ctx)
	}
}

// warmingKickSink 记账后，有预热中跳过的项就唤醒跳过恢复：少量新增或落后的标的预热很快，可能在本期算完、记账之前就已就绪，
// 只靠预热完成时的唤醒会错过它们。
type warmingKickSink struct {
	pipeline.GapSink
	kick func()
	// pending 记着已记账、还没等到 marker 的预热中跳过项：这一期的 marker 在结果写完之后才写，记账时唤醒的恢复读不到它，
	// 只能保留待办等下一轮；marker 写完后（PeriodReported）再唤醒一次，恢复才能及时发生。
	pending *atomic.Bool
}

func (s warmingKickSink) RecordGaps(ctx context.Context, entries []pipeline.GapEntry) error {
	err := s.GapSink.RecordGaps(ctx, entries)
	for _, entry := range entries {
		if entry.Reason == pipeline.SkipReasonWarming {
			if s.pending != nil {
				s.pending.Store(true)
			}
			s.kick()
			break
		}
	}
	return err
}

// PeriodReported 在周期完成标记写完后调用：之前记过预热中跳过的项就再唤醒一次。多个任务并行时标志是共享的，多唤醒一次无害。
func (s warmingKickSink) PeriodReported() {
	if s.pending != nil && s.pending.Swap(false) {
		s.kick()
	}
}

// settled 判断一条待办是否可以恢复了：发现后过了 Settle（给自动补采留时间）；预热中跳过的项在该任务预热就绪后即可恢复，
// 那一期的源数据在跳过时就已入库，只是窗口还没载入。否则重启后第一期（1h 任务是整整一根）要在预热完成后再等 5～10 分钟才补齐。
// 上游失败（截止时还没到）的项在收到覆盖该期的补齐通知后也不再等 Settle。
func settled(entry gapledger.Entry, cutoff time.Time, ready map[string]bool, filled func(string, time.Time) bool) bool {
	return !entry.FirstSeen.After(cutoff) || (entry.Reason == pipeline.SkipReasonWarming && ready[entry.Set]) ||
		(entry.Reason == pipeline.SkipReasonUpstream && filled != nil && filled(entry.Set, entry.Period))
}

// readyTasks 返回预热已就绪（全部标的窗口已载入、没有排队或在途的预热）的任务。
func (g *gapFiller) readyTasks() map[string]bool {
	if g.warmup == nil {
		return nil
	}
	ready := make(map[string]bool)
	for _, status := range g.warmup() {
		if status.State == pipeline.WarmupReady {
			ready[status.TaskID] = true
		}
	}
	return ready
}

// maxFillSubjects 是一次时序恢复最多合并的标的数。恢复区间相同的标的（同一期整体预热跳过、同一段采集中断造成的空洞）
// 合并成一次只填充重算，不逐个标的各跑一遍：重启后一期约 500 个标的整体预热跳过时，逐个跑要几百次运行、几十分钟才恢复完。
// 上限避免一次运行占住任务锁太久。
const maxFillSubjects = 100

// fillBatch 是一次恢复运行：截面因子是一个 (任务, 周期)；时序因子是同一任务里恢复区间相同的若干标的。
type fillBatch struct {
	set        string
	cross      bool
	entries    []gapledger.Entry // 截面：该周期的待办
	subjects   []string          // 时序：恢复区间相同的标的
	start, end time.Time         // 时序：恢复区间
	names      []string          // 合并进来的组名，轮转用
}

// tick 做一轮恢复：先把超期的待办记为 gave_up 并告警，再按 (任务, 标的) 与 (任务, 周期) 分组，
// 恢复区间相同的时序分组合并成一批，逐批填充。
func (g *gapFiller) tick(ctx context.Context) {
	if g.active != nil && !g.active() {
		return
	}
	g.expire()
	g.universeMemo = make(map[universeKey]universeMemoEntry)
	cutoff := g.now().UTC().Add(-g.cfg.Settle)
	ready := g.readyTasks()
	type groupKey struct {
		set, subject string
		cross        bool
		period       int64
	}
	groups := make(map[groupKey][]gapledger.Entry)
	var order []groupKey
	setCache := make(map[string]domain.EngineTask)
	lookups := make(map[string]SetLookup)
	for _, entry := range g.ledger.Pending() {
		if !settled(entry, cutoff, ready, g.filled) {
			continue // 刚发现，给自动补采一点时间
		}
		set, lookup := setCache[entry.Set], lookups[entry.Set]
		if _, looked := lookups[entry.Set]; !looked {
			set, lookup = g.sets.SetByID(entry.Set)
			setCache[entry.Set], lookups[entry.Set] = set, lookup
		}
		switch lookup {
		case SetNotReady:
			continue // 目录未加载或结果表未就绪：等待，不能当作任务已经不存在
		case SetGone:
			g.cancel([]gapledger.Entry{entry}, "task_gone")
			continue
		}
		cross := memberIsCross(set, entry.Member)
		gk := groupKey{set: entry.Set, cross: cross}
		if cross {
			gk.period = entry.Period.Unix()
		} else {
			gk.subject = entry.Subject
		}
		if _, exists := groups[gk]; !exists {
			order = append(order, gk)
		}
		groups[gk] = append(groups[gk], entry)
	}
	if g.lastTried == nil {
		g.lastTried = make(map[string]time.Time)
	}
	name := func(gk groupKey) string { return fmt.Sprintf("%s|%s|%t|%d", gk.set, gk.subject, gk.cross, gk.period) }
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if ta, tb := g.lastTried[name(a)], g.lastTried[name(b)]; !ta.Equal(tb) {
			return ta.Before(tb)
		}
		if a.set != b.set {
			return a.set < b.set
		}
		if a.cross != b.cross {
			return !a.cross
		}
		if a.subject != b.subject {
			return a.subject < b.subject
		}
		return a.period < b.period
	})
	// 按「最久没试过的优先」的顺序建批：批的位置由它最早的组决定，后面区间相同的组并进已有的批。
	type spanKey struct {
		set        string
		start, end int64
	}
	var batches []*fillBatch
	open := make(map[spanKey]*fillBatch)
	for _, gk := range order {
		if gk.cross {
			batches = append(batches, &fillBatch{set: gk.set, cross: true, entries: groups[gk], names: []string{name(gk)}})
			continue
		}
		start, end, err := g.span(setCache[gk.set].Set, groups[gk])
		if err != nil {
			log.WarnContextf(ctx, "factor_gap_fill_failed task_id=%s subject=%s error=%v", gk.set, gk.subject, err)
			continue
		}
		sk := spanKey{set: gk.set, start: start.Unix(), end: end.Unix()}
		batch := open[sk]
		if batch == nil || len(batch.subjects) >= maxFillSubjects {
			batch = &fillBatch{set: gk.set, start: start, end: end}
			open[sk] = batch
			batches = append(batches, batch)
		}
		batch.subjects = append(batch.subjects, gk.subject)
		batch.names = append(batch.names, name(gk))
	}
	if len(batches) > g.cfg.MaxGroupsPerSweep {
		batches = batches[:g.cfg.MaxGroupsPerSweep]
	}
	for _, batch := range batches {
		// 租约在一轮中途丢失时不再启动新的写入：另一个引擎已经接手，两个进程的任务锁互不约束。
		if ctx.Err() != nil || (g.active != nil && !g.active()) {
			return
		}
		for _, n := range batch.names {
			g.lastTried[n] = g.now()
		}
		set := setCache[batch.set]
		var err error
		if batch.cross {
			err = g.fillCross(ctx, set, batch.entries)
		} else {
			err = g.fillSubjects(ctx, set, batch.subjects, batch.start, batch.end)
		}
		if err != nil && ctx.Err() == nil && !errors.Is(err, recalcexec.ErrGated) {
			log.WarnContextf(ctx, "factor_gap_fill_failed task_id=%s cross=%t subjects=%d first=%s error=%v", batch.set, batch.cross, len(batch.subjects), firstSubject(batch), err)
		}
	}
}

func firstSubject(batch *fillBatch) string {
	if len(batch.subjects) > 0 {
		return batch.subjects[0]
	}
	if len(batch.entries) > 0 {
		return batch.entries[0].Subject
	}
	return ""
}

// expire 把超过 GiveUpAfter 仍未补齐的待办记为 gave_up 并告警：交给人工补采与补算。
func (g *gapFiller) expire() {
	expired, err := g.ledger.ExpireOverdue(g.cfg.GiveUpAfter)
	if err != nil {
		log.Errorf("factor_gap_ledger_expire_failed error=%v", err)
	}
	logExpired(expired, g.cfg.GiveUpAfter)
	if g.onGaveUp != nil && len(expired) > 0 {
		g.onGaveUp(len(expired))
	}
}

func logExpired(expired []gapledger.Entry, giveUp time.Duration) {
	if len(expired) == 0 {
		return
	}
	bySubject := make(map[string]int)
	for _, entry := range expired {
		bySubject[entry.Set+"/"+entry.Subject]++
	}
	for name, count := range bySubject {
		log.Errorf("factor_gap_gave_up target=%s entries=%d after=%s hint=先用 moox-cli collector kline backfill 补采缺口，再用 moox-factor-mgr-cli recalc 手工重算", name, count, giveUp)
	}
}

func memberIsCross(set domain.EngineTask, alias string) bool {
	for _, member := range set.Members {
		if member.Key == alias {
			return member.Factor.FactorType == domain.FactorTypeCrossSection
		}
	}
	return false
}

// refresh 在任务锁内按最新的任务与账本重读一组待办：丢掉已终结的，定义哈希与当前不同的记为 cancelled（definition_changed），
// 成员已不在任务里的记为 cancelled（factor_removed）。返回仍可恢复的待办与对应的成员。
func (g *gapFiller) refresh(taskID string, entries []gapledger.Entry, cross bool) (domain.EngineTask, SetLookup, []gapledger.Entry, []domain.MemberDef) {
	set, lookup := g.sets.SetByID(taskID)
	if lookup != SetFound {
		if lookup == SetGone {
			g.cancel(entries, "task_gone")
		}
		return set, lookup, nil, nil
	}
	defs := make(map[string]domain.MemberDef, len(set.Members))
	for _, member := range set.Members {
		defs[member.Key] = member
	}
	var use []gapledger.Entry
	var removed, changed []gapledger.Entry
	members := make(map[string]domain.MemberDef)
	for _, entry := range entries {
		member, ok := defs[entry.Member]
		switch {
		case !ok:
			removed = append(removed, entry)
		case domain.DefinitionHash(member.Factor) != entry.Hash:
			changed = append(changed, entry)
		case (member.Factor.FactorType == domain.FactorTypeCrossSection) != cross:
			continue
		default:
			use = append(use, entry)
			members[entry.Member] = member
		}
	}
	g.cancel(removed, "factor_removed")
	g.cancel(changed, "definition_changed")
	list := make([]domain.MemberDef, 0, len(members))
	for _, member := range members {
		list = append(list, member)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return set, SetFound, use, list
}

func (g *gapFiller) cancel(entries []gapledger.Entry, reason string) {
	if len(entries) == 0 {
		return
	}
	refs := make([]gapledger.Ref, 0, len(entries))
	for _, entry := range entries {
		refs = append(refs, entry.Ref())
	}
	if err := g.ledger.MarkCancelled(refs, reason); err != nil {
		log.Errorf("factor_gap_ledger_cancel_failed reason=%s error=%v", reason, err)
	}
}

// span 返回一组待办的恢复区间 [start, end)：最早周期起，最多 MaxSpan 个周期，终点是最后一个周期加一个周期。
func (g *gapFiller) span(set domain.FactorTask, entries []gapledger.Entry) (time.Time, time.Time, error) {
	duration, err := g.clock.Duration(set.Freq)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start, last := entries[0].Period, entries[0].Period
	for _, entry := range entries {
		if entry.Period.Before(start) {
			start = entry.Period
		}
		if entry.Period.After(last) {
			last = entry.Period
		}
	}
	end := last.Add(duration)
	if limit := start.Add(time.Duration(g.cfg.MaxSpan) * duration); end.After(limit) {
		end = limit
	}
	return start, end, nil
}

// fillSubjects 在 [start, end) 上一次恢复一个任务里若干标的的时间序列因子待办。
// 统一数据集（同一数据集里有现货、永续两个市场的序列）按待办周期读结果数据集 marker 里冻结的 universe_series：
// 每个周期的期望序列就是当期实际存在的序列，恢复时每条期望序列都要写成才确认（永续晚到、这一期只有现货行时记失败，待办保留），
// 已下架的序列不在之后各期的冻结全集里，不会让现货的待办永远确认不了。冻结全集相同的相邻周期合并成一次运行；
// 单序列数据集、读不到 marker 或 marker 里没有序列全集的周期，保持原来的一次运行，期望序列由数据层兜底推出。
func (g *gapFiller) fillSubjects(ctx context.Context, set domain.EngineTask, subjects []string, start, end time.Time) error {
	taskID := set.Set.TaskID
	pending := func() []gapledger.Entry { return g.ledger.PendingFor(taskID, subjects) }
	runs, err := g.frozenRuns(ctx, set, subjects, pending(), start, end)
	if err != nil {
		return fmt.Errorf("read frozen universe: %w", err)
	}
	for _, run := range runs {
		if err := g.fillRun(ctx, taskID, pending, run); err != nil {
			return err
		}
	}
	return nil
}

// fillRun 是时序恢复的一次运行：[run.start, run.end) 上一个冻结全集（series 为空表示没有冻结全集）。
type fillRun struct {
	start, end time.Time
	series     []seriesset.Series
}

func (g *gapFiller) fillRun(ctx context.Context, taskID string, pending func() []gapledger.Entry, run fillRun) error {
	source := func(ctx context.Context) (recalcexec.Selection, error) {
		// 在任务锁内重读：用最新的待办（含最新的 skipID）与最新的任务、因子定义。
		if g.active != nil && !g.active() {
			return recalcexec.Selection{}, nil // 取得任务锁后租约已丢失：不运行
		}
		current, lookup, use, members := g.refresh(taskID, pending(), false)
		if lookup != SetFound || len(use) == 0 {
			return recalcexec.Selection{}, nil
		}
		columns, err := g.source.DatasetColumns(ctx, current.Set.SpaceID, current.Set.SourceDatasetID)
		if err != nil {
			return recalcexec.Selection{}, fmt.Errorf("list source dataset columns: %w", err)
		}
		subjects := entrySubjects(use)
		return recalcexec.Selection{
			Set: current.Set, Members: members, Subjects: subjects, Series: seriesOfSubjects(run.series, subjects), Columns: columns,
			FillOnly: true, FillCarry: anyWarming(use),
		}, nil
	}
	after := func(selection recalcexec.Selection, outcome pipeline.Outcome, runErr error) {
		if runErr != nil {
			return
		}
		g.confirm(pending(), selection, outcome, run.start, run.end)
	}
	_, err := g.exec.RunChunkWith(ctx, source, run.start, run.end, after)
	return err
}

// frozenRuns 把 [start, end) 切成按冻结全集分段的运行。非统一数据集（或没有任何周期有冻结全集、也没有要保留的周期）返回原区间的一次运行。
// 否则只覆盖有待办的周期：相邻的、冻结全集相同的周期合并（先按本批标的过滤全集再比较，别的标的上下架不切碎批次；不相邻的周期不合并），
// 没有冻结全集的周期单独成段（期望序列由数据层兜底）。只处理时序成员的待办：截面成员由 fillCross 按周期恢复。
// 某一期读 marker 失败，或读不到 marker 但待办刚记下不足 Settle（marker 可能还没写），这一期不运行、待办保留到下一轮，其余各期照常。
func (g *gapFiller) frozenRuns(ctx context.Context, set domain.EngineTask, subjects []string, entries []gapledger.Entry, start, end time.Time) ([]fillRun, error) {
	whole := []fillRun{{start: start, end: end}}
	if g.unified == nil || g.universe == nil {
		return whole, nil
	}
	multi, err := g.unified(ctx, set.Set)
	if err != nil || !multi {
		return whole, err
	}
	duration, err := g.clock.Duration(set.Set.Freq)
	if err != nil {
		return nil, err
	}
	type periodInfo struct {
		trigger   string
		firstSeen time.Time // 这一期待办里最晚记下的时刻
	}
	infos := make(map[int64]*periodInfo)
	var periods []int64
	for _, entry := range entries {
		if entry.Period.Before(start) || !entry.Period.Before(end) || memberIsCross(set, entry.Member) {
			continue
		}
		at := entry.Period.Unix()
		info := infos[at]
		if info == nil {
			info = &periodInfo{}
			infos[at] = info
			periods = append(periods, at)
		}
		if info.trigger == "" {
			info.trigger = entry.Trigger
		}
		if entry.FirstSeen.After(info.firstSeen) {
			info.firstSeen = entry.FirstSeen
		}
	}
	sort.Slice(periods, func(i, j int) bool { return periods[i] < periods[j] })
	var runs []fillRun
	anyFrozen, anyHeld := false, false
	for _, at := range periods {
		info := infos[at]
		var series []seriesset.Series
		if info.trigger != "" {
			universe, found, err := g.computedUniverse(ctx, set.Set, info.trigger, at)
			if err != nil {
				log.WarnContextf(ctx, "factor_gap_universe_read_failed task_id=%s period=%d error=%v", set.Set.TaskID, at, err)
				anyHeld = true
				continue // 只让读失败的这一期留待下一轮
			}
			if found {
				series = seriesOfSubjects(universe.Series, subjects)
			} else if g.markerMayBeLate(info.firstSeen) {
				anyHeld = true
				continue // marker 可能还没写：待办保留，下一轮再试
			}
		}
		anyFrozen = anyFrozen || len(series) > 0
		period := time.Unix(at, 0).UTC()
		if last := len(runs) - 1; last >= 0 && runs[last].end.Equal(period) && sameSeries(runs[last].series, series) {
			runs[last].end = period.Add(duration)
			continue
		}
		runs = append(runs, fillRun{start: period, end: period.Add(duration), series: series})
	}
	if !anyFrozen && !anyHeld {
		return whole, nil
	}
	return runs, nil
}

// sameSeries 判断两份序列全集是否相同（顺序无关）。
func sameSeries(a, b []seriesset.Series) bool {
	left, right := seriesset.Union(a), seriesset.Union(b)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// seriesOfSubjects 返回序列里属于这些标的的部分。
func seriesOfSubjects(values []seriesset.Series, subjects []string) []seriesset.Series {
	if len(values) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		wanted[subject] = struct{}{}
	}
	out := make([]seriesset.Series, 0, len(values))
	for _, value := range values {
		if _, ok := wanted[value.SubjectID]; ok {
			out = append(out, value)
		}
	}
	return out
}

// anyWarming 报告待办里是否有预热中或上游失败而跳过的项：那一期实时路径没有写任何行，恢复时 carry 列也要按源数据写入。
// 空洞跳过的那一期实时路径已经写过 carry 列，恢复只补因子列。
func anyWarming(entries []gapledger.Entry) bool {
	for _, entry := range entries {
		if entry.Reason == pipeline.SkipReasonWarming || entry.Reason == pipeline.SkipReasonUpstream {
			return true
		}
	}
	return false
}

// entrySubjects 返回待办里出现的标的（已排序、去重）。
func entrySubjects(entries []gapledger.Entry) []string {
	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, ok := seen[entry.Subject]; !ok {
			seen[entry.Subject] = struct{}{}
			out = append(out, entry.Subject)
		}
	}
	sort.Strings(out)
	return out
}

// fillCross 恢复一个任务里一个周期的截面因子待办：用该周期 marker 里冻结的全集重算，不用当前标的池，
// 也不把单个标的当作完整截面。marker 读不到（例如数据集被删）无法冻结全集，记 gave_up 交给人工。
func (g *gapFiller) fillCross(ctx context.Context, set domain.EngineTask, entries []gapledger.Entry) error {
	period := entries[0].Period
	trigger := ""
	for _, entry := range entries {
		if entry.Trigger != "" {
			trigger = entry.Trigger
			break
		}
	}
	refs := make([]gapledger.Ref, 0, len(entries))
	for _, entry := range entries {
		refs = append(refs, entry.Ref())
	}
	if trigger == "" {
		return g.ledger.MarkGaveUp(refs, "universe_unavailable")
	}
	universe, found, err := g.computedUniverse(ctx, set.Set, trigger, period.Unix())
	if err != nil {
		return fmt.Errorf("read frozen universe: %w", err) // 读取失败：保持待恢复，下轮再试
	}
	if !found {
		// marker 可能还没写（待办是实时周期跳过时记下的，marker 在结果写完之后才写）：刚记下不足 Settle 先保留，下一轮再试。
		for _, entry := range entries {
			if g.markerMayBeLate(entry.FirstSeen) {
				return nil
			}
		}
	}
	if !found || len(universe.Subjects) == 0 {
		return g.ledger.MarkGaveUp(refs, "universe_unavailable")
	}
	duration, err := g.clock.Duration(set.Set.Freq)
	if err != nil {
		return err
	}
	end := period.Add(duration)
	taskID := set.Set.TaskID
	periodStart := period
	source := func(ctx context.Context) (recalcexec.Selection, error) {
		if g.active != nil && !g.active() {
			return recalcexec.Selection{}, nil
		}
		var pending []gapledger.Entry
		for _, entry := range g.ledger.Pending() {
			if entry.Set == taskID && entry.Period.Equal(periodStart) {
				pending = append(pending, entry)
			}
		}
		current, lookup, use, members := g.refresh(taskID, pending, true)
		if lookup != SetFound || len(use) == 0 {
			return recalcexec.Selection{}, nil
		}
		columns, err := g.source.DatasetColumns(ctx, current.Set.SpaceID, current.Set.SourceDatasetID)
		if err != nil {
			return recalcexec.Selection{}, fmt.Errorf("list source dataset columns: %w", err)
		}
		// 统一数据集：冻结全集按序列（标的、series_tag）判断，缺一个市场的币时只跳过那个市场。
		return recalcexec.Selection{Set: current.Set, Members: members, Subjects: universe.Subjects, Series: universe.Series, Columns: columns,
			FillOnly: true, FillCarry: anyWarming(use)}, nil
	}
	after := func(selection recalcexec.Selection, outcome pipeline.Outcome, runErr error) {
		if runErr != nil {
			return
		}
		var pending []gapledger.Entry
		for _, entry := range g.ledger.Pending() {
			if entry.Set == taskID && entry.Period.Equal(periodStart) {
				pending = append(pending, entry)
			}
		}
		g.confirm(pending, selection, outcome, periodStart, end)
	}
	_, err = g.exec.RunChunkWith(ctx, source, period, end, after)
	return err
}

// confirm 在任务锁内确认：Selection 里的因子、区间内、逐项结果为 written 的待办追加 repaired。
// gap 与 failed 保持待恢复，下一轮再试。
func (g *gapFiller) confirm(current []gapledger.Entry, selection recalcexec.Selection, outcome pipeline.Outcome, start, end time.Time) {
	selected := make(map[string]struct{}, len(selection.Members))
	for _, member := range selection.Members {
		selected[member.Key] = struct{}{}
	}
	var repaired []gapledger.Ref
	for _, entry := range current {
		if _, ok := selected[entry.Member]; !ok || entry.Period.Before(start) || !entry.Period.Before(end) {
			continue
		}
		if outcome.Items[pipeline.ItemKey{Member: entry.Member, Subject: entry.Subject, Period: entry.Period}] == pipeline.ItemWritten {
			repaired = append(repaired, entry.Ref())
		}
	}
	if err := g.ledger.MarkRepaired(repaired); err != nil {
		log.Errorf("factor_gap_ledger_repaired_failed entries=%d error=%v", len(repaired), err)
		return
	}
	if len(repaired) == 0 {
		return
	}
	log.Infof("factor_gap_repaired task_id=%s entries=%d window=%s/%s", selection.Set.TaskID, len(repaired), periodclock.FormatPeriod(selection.Set.SpaceID, selection.Set.Freq, start), periodclock.FormatPeriod(selection.Set.SpaceID, selection.Set.Freq, end))
	if g.onRepaired == nil {
		return
	}
	byPeriod := make(map[int64]*RepairedPeriod)
	for _, ref := range repaired {
		at := ref.Period.UTC().Unix()
		if byPeriod[at] == nil {
			byPeriod[at] = &RepairedPeriod{Period: time.Unix(at, 0).UTC()}
		}
		byPeriod[at].Repaired = append(byPeriod[at].Repaired, pipeline.ItemKey{Member: ref.Member, Subject: ref.Subject, Period: ref.Period})
	}
	// 这次重算没有写成的项也带上：截面恢复重算整个冻结全集，账本里只有当初有洞的标的；别的标的这次没写成，
	// 而实时路径当时整因子跳过、也没写，这一期的结果仍然缺它们，不能因为账本清空就记为完成。
	for key, status := range outcome.Items {
		if _, ok := selected[key.Member]; !ok || status == pipeline.ItemWritten {
			continue
		}
		if period := byPeriod[key.Period.UTC().Unix()]; period != nil {
			period.Unwritten = append(period.Unwritten, key)
		}
	}
	for _, period := range byPeriod {
		period.Done = !g.ledger.HasPendingIn(selection.Set.TaskID, period.Period)
		g.onRepaired(selection.Set.TaskID, *period)
	}
}

// RepairedPeriod 是一批恢复对一个周期的结果：Repaired 是补齐的待办，Unwritten 是这次重算没有写成的项，
// Done 表示账本里这一期已经没有待恢复项。
type RepairedPeriod struct {
	Period              time.Time
	Repaired, Unwritten []pipeline.ItemKey
	Done                bool
}
