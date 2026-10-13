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

// 空洞恢复：引擎每隔一段时间扫描账本里待恢复的 (标的, 周期, 因子)，在进程内用补算执行器做「只填充」重算：
// 窗口检查通过才计算并写入，仍有洞就什么都不写（该键最初跳过时已写成 NULL）。写入确认后追加 repaired。
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
}

// Run 按 SweepInterval 轮询直到 ctx 结束。
func (g *gapFiller) Run(ctx context.Context) {
	ticker := time.NewTicker(g.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.tick(ctx)
		}
	}
}

// tick 做一轮恢复：先把超期的待办记为 gave_up 并告警，再按 (集合, 标的) 与 (集合, 周期) 分组逐组填充。
func (g *gapFiller) tick(ctx context.Context) {
	if g.active != nil && !g.active() {
		return
	}
	g.expire()
	cutoff := g.now().UTC().Add(-g.cfg.Settle)
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
		if entry.FirstSeen.After(cutoff) {
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
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
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
	if len(order) > g.cfg.MaxGroupsPerSweep {
		order = order[:g.cfg.MaxGroupsPerSweep]
	}
	for _, gk := range order {
		if ctx.Err() != nil {
			return
		}
		set := setCache[gk.set]
		var err error
		if gk.cross {
			err = g.fillCross(ctx, set, groups[gk])
		} else {
			err = g.fillSubject(ctx, set, gk.subject, groups[gk])
		}
		if err != nil && ctx.Err() == nil {
			log.WarnContextf(ctx, "factor_gap_fill_failed set_id=%s subject=%s cross=%t error=%v", gk.set, gk.subject, gk.cross, err)
		}
	}
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

// fillSubject 恢复一个集合里一个标的的时间序列因子待办。
func (g *gapFiller) fillSubject(ctx context.Context, set domain.EngineSet, subject string, entries []gapledger.Entry) error {
	start, end, err := g.span(set.Set, entries)
	if err != nil {
		return err
	}
	setID := set.Set.SetID
	source := func(ctx context.Context) (recalcexec.Selection, error) {
		// 在集合锁内重读：用最新的待办（含最新的 skipID）与最新的集合、因子定义。
		current, lookup, use, factors := g.refresh(setID, g.ledger.PendingFor(setID, subject), false)
		if lookup != SetFound || len(use) == 0 {
			return recalcexec.Selection{}, nil
		}
		columns, err := g.source.DatasetColumns(ctx, current.Set.SpaceID, current.Set.SourceDatasetID)
		if err != nil {
			return recalcexec.Selection{}, fmt.Errorf("list source dataset columns: %w", err)
		}
		return recalcexec.Selection{Set: current.Set, Factors: factors, Subjects: []string{subject}, Columns: columns, FillOnly: true}, nil
	}
	after := func(selection recalcexec.Selection, outcome pipeline.Outcome, runErr error) {
		if runErr != nil {
			return
		}
		g.confirm(g.ledger.PendingFor(setID, subject), selection, outcome, start, end)
	}
	_, err = g.exec.RunChunkWith(ctx, source, start, end, after)
	return err
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
		return recalcexec.Selection{Set: current.Set, Factors: factors, Subjects: universe, Columns: columns, FillOnly: true}, nil
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
	if len(repaired) > 0 {
		log.Infof("factor_gap_repaired set_id=%s entries=%d window=%s/%s", selection.Set.SetID, len(repaired), start.Format(time.RFC3339), end.Format(time.RFC3339))
	}
}
