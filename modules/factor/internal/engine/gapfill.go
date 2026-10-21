package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/gapledger"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"trpc.group/trpc-go/trpc-go/log"
)

// 跳过恢复：引擎每隔一段时间扫描账本里待恢复的 (标的, 周期, 因子)，在进程内用补算执行器做「只填充」重算：
// 窗口检查通过才计算并写入，仍有洞就什么都不写（该键最初跳过时已写成 NULL）。写入确认后追加 repaired。
// 待办有两种来源：回看窗口有空洞，或者实时周期里标的还在预热（reason=warming，本期没有算）。
// 设计见 docs/计划/因子窗口空洞防护详细设计.md 2.6。

type setByID interface {
	SetByID(setID string) (domain.EngineSet, SetLookup)
}

type chunkWithRunner interface {
	RunChunkWith(ctx context.Context, source recalcexec.SelectionSource, start, end time.Time, after func(recalcexec.Selection, pipeline.Outcome, error)) (pipeline.Outcome, error)
}

type universeReader interface {
	ComputedUniverse(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) ([]string, bool, error)
}

type gapFiller struct {
	ledger   *gapledger.Ledger
	sets     setByID
	source   SourceDataset
	exec     chunkWithRunner
	universe universeReader
	clock    periodclock.Clock
	active   func() bool
	cfg      GapConfig
	now      func() time.Time
	// onGaveUp 在条目被记为 gave_up 时调用（指标用，可为 nil）。
	onGaveUp func(entries int)
	// onRepaired 在一批待办确认补齐后按周期调用（可为 nil），在集合锁内。引擎据此把补齐的最新一期记入运行摘要。
	onRepaired func(setID string, period RepairedPeriod)
	// kick 让恢复循环立刻做一轮（预热完成、记入预热中跳过的项时），为 nil 时只按 SweepInterval 轮询。
	kick <-chan struct{}
	// warmup 返回各集合的预热状态（可为 nil）：已就绪集合里因预热中跳过的待办不再等 Settle。
	warmup func() []pipeline.WarmupStatus
	// filled 判断某集合某一期是否已收到补齐通知（可为 nil）。
	filled func(setID string, period time.Time) bool
	// lastTried 记录每个组最近一次尝试的时间：一轮只处理 MaxGroupsPerSweep 个组，按「最久没试过的优先」轮转，
	// 前面一直补不上的组不会让后面已经补齐的组长期得不到执行机会。
	lastTried map[string]time.Time
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
}

func (s warmingKickSink) RecordGaps(ctx context.Context, entries []pipeline.GapEntry) error {
	err := s.GapSink.RecordGaps(ctx, entries)
	for _, entry := range entries {
		if entry.Reason == pipeline.SkipReasonWarming {
			s.kick()
			break
		}
	}
	return err
}

// settled 判断一条待办是否可以恢复了：发现后过了 Settle（给自动补采留时间）；预热中跳过的项在该集合预热就绪后即可恢复，
// 那一期的源数据在跳过时就已入库，只是窗口还没载入。否则重启后第一期（1h 集合是整整一根）要在预热完成后再等 5～10 分钟才补齐。
// 上游失败（截止时还没到）的项在收到覆盖该期的补齐通知后也不再等 Settle。
func settled(entry gapledger.Entry, cutoff time.Time, ready map[string]bool, filled func(string, time.Time) bool) bool {
	return !entry.FirstSeen.After(cutoff) || (entry.Reason == pipeline.SkipReasonWarming && ready[entry.Set]) ||
		(entry.Reason == pipeline.SkipReasonUpstream && filled != nil && filled(entry.Set, entry.Period))
}

// readySets 返回预热已就绪（全部标的窗口已载入、没有排队或在途的预热）的集合。
func (g *gapFiller) readySets() map[string]bool {
	if g.warmup == nil {
		return nil
	}
	ready := make(map[string]bool)
	for _, status := range g.warmup() {
		if status.State == pipeline.WarmupReady {
			ready[status.SetID] = true
		}
	}
	return ready
}

// maxFillSubjects 是一次时序恢复最多合并的标的数。恢复区间相同的标的（同一期整体预热跳过、同一段采集中断造成的空洞）
// 合并成一次只填充重算，不逐个标的各跑一遍：重启后一期约 500 个标的整体预热跳过时，逐个跑要几百次运行、几十分钟才恢复完。
// 上限避免一次运行占住集合锁太久。
const maxFillSubjects = 100

// fillBatch 是一次恢复运行：截面因子是一个 (集合, 周期)；时序因子是同一集合里恢复区间相同的若干标的。
type fillBatch struct {
	set        string
	cross      bool
	entries    []gapledger.Entry // 截面：该周期的待办
	subjects   []string          // 时序：恢复区间相同的标的
	start, end time.Time         // 时序：恢复区间
	names      []string          // 合并进来的组名，轮转用
}

// tick 做一轮恢复：先把超期的待办记为 gave_up 并告警，再按 (集合, 标的) 与 (集合, 周期) 分组，
// 恢复区间相同的时序分组合并成一批，逐批填充。
func (g *gapFiller) tick(ctx context.Context) {
	if g.active != nil && !g.active() {
		return
	}
	g.expire()
	cutoff := g.now().UTC().Add(-g.cfg.Settle)
	ready := g.readySets()
	type groupKey struct {
		set, subject string
		cross        bool
		period       int64
	}
	groups := make(map[groupKey][]gapledger.Entry)
	var order []groupKey
	setCache := make(map[string]domain.EngineSet)
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
			continue // 目录未加载或结果表未就绪：等待，不能当作集合已经不存在
		case SetGone:
			g.cancel([]gapledger.Entry{entry}, "set_gone")
			continue
		}
		cross := factorIsCross(set, entry.FactorID)
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
			log.WarnContextf(ctx, "factor_gap_fill_failed set_id=%s subject=%s error=%v", gk.set, gk.subject, err)
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
		// 租约在一轮中途丢失时不再启动新的写入：另一个引擎已经接手，两个进程的集合锁互不约束。
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
		if err != nil && ctx.Err() == nil {
			log.WarnContextf(ctx, "factor_gap_fill_failed set_id=%s cross=%t subjects=%d first=%s error=%v", batch.set, batch.cross, len(batch.subjects), firstSubject(batch), err)
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

func factorIsCross(set domain.EngineSet, factorID string) bool {
	for _, factor := range set.Factors {
		if factor.FactorID == factorID {
			return factor.FactorType == domain.FactorTypeCrossSection
		}
	}
	return false
}

// refresh 在集合锁内按最新的集合与账本重读一组待办：丢掉已终结的，定义哈希与当前不同的记为 cancelled（definition_changed），
// 因子已不在集合里的记为 cancelled（factor_removed）。返回仍可恢复的待办与对应的因子定义。
func (g *gapFiller) refresh(setID string, entries []gapledger.Entry, cross bool) (domain.EngineSet, SetLookup, []gapledger.Entry, []domain.FactorDef) {
	set, lookup := g.sets.SetByID(setID)
	if lookup != SetFound {
		if lookup == SetGone {
			g.cancel(entries, "set_gone")
		}
		return set, lookup, nil, nil
	}
	defs := make(map[string]domain.FactorDef, len(set.Factors))
	for _, factor := range set.Factors {
		defs[factor.FactorID] = factor
	}
	var use []gapledger.Entry
	var removed, changed []gapledger.Entry
	factors := make(map[string]domain.FactorDef)
	for _, entry := range entries {
		def, ok := defs[entry.FactorID]
		switch {
		case !ok:
			removed = append(removed, entry)
		case domain.DefinitionHash(def) != entry.Hash:
			changed = append(changed, entry)
		case (def.FactorType == domain.FactorTypeCrossSection) != cross:
			continue
		default:
			use = append(use, entry)
			factors[entry.FactorID] = def
		}
	}
	g.cancel(removed, "factor_removed")
	g.cancel(changed, "definition_changed")
	list := make([]domain.FactorDef, 0, len(factors))
	for _, def := range factors {
		list = append(list, def)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].FactorID < list[j].FactorID })
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
func (g *gapFiller) span(set domain.FactorSet, entries []gapledger.Entry) (time.Time, time.Time, error) {
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

// fillSubjects 在 [start, end) 上一次恢复一个集合里若干标的的时间序列因子待办。
func (g *gapFiller) fillSubjects(ctx context.Context, set domain.EngineSet, subjects []string, start, end time.Time) error {
	setID := set.Set.SetID
	pending := func() []gapledger.Entry { return g.ledger.PendingFor(setID, subjects) }
	source := func(ctx context.Context) (recalcexec.Selection, error) {
		// 在集合锁内重读：用最新的待办（含最新的 skipID）与最新的集合、因子定义。
		if g.active != nil && !g.active() {
			return recalcexec.Selection{}, nil // 取得集合锁后租约已丢失：不运行
		}
		current, lookup, use, factors := g.refresh(setID, pending(), false)
		if lookup != SetFound || len(use) == 0 {
			return recalcexec.Selection{}, nil
		}
		columns, err := g.source.DatasetColumns(ctx, current.Set.SpaceID, current.Set.SourceDatasetID)
		if err != nil {
			return recalcexec.Selection{}, fmt.Errorf("list source dataset columns: %w", err)
		}
		return recalcexec.Selection{
			Set: current.Set, Factors: factors, Subjects: entrySubjects(use), Columns: columns, FillOnly: true, FillCarry: anyWarming(use),
		}, nil
	}
	after := func(selection recalcexec.Selection, outcome pipeline.Outcome, runErr error) {
		if runErr != nil {
			return
		}
		g.confirm(pending(), selection, outcome, start, end)
	}
	_, err := g.exec.RunChunkWith(ctx, source, start, end, after)
	return err
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

// fillCross 恢复一个集合里一个周期的截面因子待办：用该周期 marker 里冻结的全集重算，不用当前标的池，
// 也不把单个标的当作完整截面。marker 读不到（例如数据集被删）无法冻结全集，记 gave_up 交给人工。
func (g *gapFiller) fillCross(ctx context.Context, set domain.EngineSet, entries []gapledger.Entry) error {
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
	universe, found, err := g.universe.ComputedUniverse(ctx, set.Set.SpaceID, set.Set.ResultDatasetID, trigger, period.Unix())
	if err != nil {
		return fmt.Errorf("read frozen universe: %w", err) // 读取失败：保持待恢复，下轮再试
	}
	if !found || len(universe) == 0 {
		return g.ledger.MarkGaveUp(refs, "universe_unavailable")
	}
	duration, err := g.clock.Duration(set.Set.Freq)
	if err != nil {
		return err
	}
	end := period.Add(duration)
	setID := set.Set.SetID
	periodStart := period
	source := func(ctx context.Context) (recalcexec.Selection, error) {
		if g.active != nil && !g.active() {
			return recalcexec.Selection{}, nil
		}
		var pending []gapledger.Entry
		for _, entry := range g.ledger.Pending() {
			if entry.Set == setID && entry.Period.Equal(periodStart) {
				pending = append(pending, entry)
			}
		}
		current, lookup, use, factors := g.refresh(setID, pending, true)
		if lookup != SetFound || len(use) == 0 {
			return recalcexec.Selection{}, nil
		}
		columns, err := g.source.DatasetColumns(ctx, current.Set.SpaceID, current.Set.SourceDatasetID)
		if err != nil {
			return recalcexec.Selection{}, fmt.Errorf("list source dataset columns: %w", err)
		}
		return recalcexec.Selection{Set: current.Set, Factors: factors, Subjects: universe, Columns: columns, FillOnly: true, FillCarry: anyWarming(use)}, nil
	}
	after := func(selection recalcexec.Selection, outcome pipeline.Outcome, runErr error) {
		if runErr != nil {
			return
		}
		var pending []gapledger.Entry
		for _, entry := range g.ledger.Pending() {
			if entry.Set == setID && entry.Period.Equal(periodStart) {
				pending = append(pending, entry)
			}
		}
		g.confirm(pending, selection, outcome, periodStart, end)
	}
	_, err = g.exec.RunChunkWith(ctx, source, period, end, after)
	return err
}

// confirm 在集合锁内确认：Selection 里的因子、区间内、逐项结果为 written 的待办追加 repaired。
// gap 与 failed 保持待恢复，下一轮再试。
func (g *gapFiller) confirm(current []gapledger.Entry, selection recalcexec.Selection, outcome pipeline.Outcome, start, end time.Time) {
	selected := make(map[string]struct{}, len(selection.Factors))
	for _, factor := range selection.Factors {
		selected[factor.FactorID] = struct{}{}
	}
	var repaired []gapledger.Ref
	for _, entry := range current {
		if _, ok := selected[entry.FactorID]; !ok || entry.Period.Before(start) || !entry.Period.Before(end) {
			continue
		}
		if outcome.Items[pipeline.ItemKey{FactorID: entry.FactorID, Subject: entry.Subject, Period: entry.Period}] == pipeline.ItemWritten {
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
	log.Infof("factor_gap_repaired set_id=%s entries=%d window=%s/%s", selection.Set.SetID, len(repaired), start.Format(time.RFC3339), end.Format(time.RFC3339))
	if g.onRepaired == nil {
		return
	}
	byPeriod := make(map[int64]*RepairedPeriod)
	for _, ref := range repaired {
		at := ref.Period.UTC().Unix()
		if byPeriod[at] == nil {
			byPeriod[at] = &RepairedPeriod{Period: time.Unix(at, 0).UTC()}
		}
		byPeriod[at].Repaired = append(byPeriod[at].Repaired, pipeline.ItemKey{FactorID: ref.FactorID, Subject: ref.Subject, Period: ref.Period})
	}
	// 这次重算没有写成的项也带上：截面恢复重算整个冻结全集，账本里只有当初有洞的标的；别的标的这次没写成，
	// 而实时路径当时整因子跳过、也没写，这一期的结果仍然缺它们，不能因为账本清空就记为完成。
	for key, status := range outcome.Items {
		if _, ok := selected[key.FactorID]; !ok || status == pipeline.ItemWritten {
			continue
		}
		if period := byPeriod[key.Period.UTC().Unix()]; period != nil {
			period.Unwritten = append(period.Unwritten, key)
		}
	}
	for _, period := range byPeriod {
		period.Done = !g.ledger.HasPendingIn(selection.Set.SetID, period.Period)
		g.onRepaired(selection.Set.SetID, *period)
	}
}

// RepairedPeriod 是一批恢复对一个周期的结果：Repaired 是补齐的待办，Unwritten 是这次重算没有写成的项，
// Done 表示账本里这一期已经没有待恢复项。
type RepairedPeriod struct {
	Period              time.Time
	Repaired, Unwritten []pipeline.ItemKey
	Done                bool
}
