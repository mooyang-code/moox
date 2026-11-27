package localdata

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/seriessync"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

// Load 实现 pipeline.Data。
func (d *Data) Load(ctx context.Context, plan pipeline.Plan) (pipeline.DataResult, error) {
	src := sourceOf(plan.Set)
	st := d.lookup(src)
	subjects := append([]string(nil), plan.Available...)
	sort.Strings(subjects)
	if plan.Mode == pipeline.ModeLive {
		return d.loadLive(ctx, st, plan, subjects)
	}
	return d.loadRange(ctx, st, plan, subjects, plan.HistorySubjects)
}

// ExpectedSubjects 实现 pipeline.SubjectExpecter：没有冻结全集的补算类计划（人工补算、失效重算、读不到 marker 的待办）
// 的期望标的；带着冻结全集的恢复路径不走这里。只排除本地最早一根不早于计划区间终点的标的（区间结束时还没上市），否则补算上市前的
// 区间时新股被当成缺数，严格截面因子整段跳过，老标的已有的值被写成 NULL。不做下架推测；本地没有数据的期望标的照常保留，按缺数处理。
// 这个数据源在本地一个标的都没有时返回 ErrNoLocalData：补算只读本地，这时什么都算不出来。
func (d *Data) ExpectedSubjects(ctx context.Context, plan pipeline.Plan) ([]string, error) {
	if len(plan.Expected) == 0 {
		return nil, nil
	}
	src := sourceOf(plan.Set)
	local, err := d.cfg.Store.Subjects(src)
	if err != nil {
		return nil, err
	}
	if len(local) == 0 {
		return nil, fmt.Errorf("%w（%s/%s %s）", ErrNoLocalData, src.SpaceID, src.DatasetID, src.Freq)
	}
	out := make([]string, 0, len(plan.Expected))
	for _, subject := range plan.Expected {
		bounds, has, err := d.cfg.Store.Bounds(src, subject)
		if err != nil {
			return nil, err
		}
		if has && bounds.Earliest.Unix() >= plan.TargetEnd.Unix() {
			continue
		}
		out = append(out, subject)
	}
	return out, nil
}

func (d *Data) loadLive(ctx context.Context, st *source, plan pipeline.Plan, subjects []string) (pipeline.DataResult, error) {
	res, fromDisk, err := d.loadLiveWindow(ctx, st, plan, subjects)
	if err != nil || !fromDisk {
		return res, err
	}
	// 目标周期早于窗口的最新一根（追赶、补算更早的周期）：窗口只保留到最新为止的回看根数，截止目标周期的回看窗口不全，
	// 改读本地磁盘（同步已经写完），不能用不完整的窗口算出看似正常的值。同步失败的标的不能当作可用。
	failed := map[string]struct{}{}
	for _, s := range res.Failed {
		failed[s] = struct{}{}
	}
	var ok []string
	for _, s := range subjects {
		if _, bad := failed[s]; !bad {
			ok = append(ok, s)
		}
	}
	// observed 成员：同步失败的标的本期不可用，但本地历史仍参与历史截面。
	history := plan.HistorySubjects
	if plan.Observed() {
		history = withHistory(history, sortedKeys(failed))
	}
	out, err := d.loadRange(ctx, st, plan, ok, history)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	for s := range failed {
		out.Failed = append(out.Failed, s)
	}
	sort.Strings(out.Failed)
	return out, nil
}

// loadLiveWindow 同步本期数据并从内存窗口取输入；目标周期早于窗口最新一根时返回 fromDisk=true，由调用方改读磁盘。
func (d *Data) loadLiveWindow(ctx context.Context, st *source, plan pipeline.Plan, subjects []string) (res pipeline.DataResult, fromDisk bool, err error) {
	// 窗口还没载入完成（或正在重建）：不等，本期按预热处理。
	if st == nil || !st.ready.Load() {
		return pipeline.DataResult{Warming: subjects}, false, nil
	}
	// 分段计时：持锁等待、等上一期写盘、取数（读本地边界、取变更日志链头、并行读批次）、写本地，结束时记一行。
	var timing struct {
		lock, settle, pull, commit time.Duration
		staged                     seriessync.PullTiming
	}
	loadStarted := time.Now()
	defer func() {
		log.Infof("factor_live_load task=%s source=%s total=%s lock_wait=%s settle=%s pull=%s bounds=%s head=%s fetch=%s batches=%d pulled=%d commit=%s",
			plan.Set.TaskID, st.spec.Source, time.Since(loadStarted).Round(time.Millisecond), timing.lock.Round(time.Millisecond), timing.settle.Round(time.Millisecond),
			timing.pull.Round(time.Millisecond), timing.staged.Bounds.Round(time.Millisecond), timing.staged.Head.Round(time.Millisecond),
			timing.staged.Fetch.Round(time.Millisecond), timing.staged.Batches, timing.staged.Subjects, timing.commit.Round(time.Millisecond))
	}()
	d.lockSource(st, "计算前读取")
	defer st.mu.Unlock()
	timing.lock = time.Since(loadStarted)
	if !st.ready.Load() || st.window == nil {
		return pipeline.DataResult{Warming: subjects}, false, nil
	}
	step, err := seriesstore.Step(plan.Set.Freq)
	if err != nil {
		return pipeline.DataResult{}, false, err
	}
	target := plan.TargetEnd.Add(-step)

	// 上一期的热路径写盘必须先落完：本期的拉取按本地 Latest 决定从哪里拉。
	began := time.Now()
	d.settleFlush(st)
	timing.settle = time.Since(began)

	began = time.Now()
	staged, err := d.syncer.PullStaged(ctx, st.spec, subjects, plan.TargetEnd)
	timing.pull = time.Since(began)
	if err != nil {
		return pipeline.DataResult{}, false, err
	}
	timing.staged = staged.Timing
	failed := map[string]struct{}{}
	for _, s := range staged.Failed {
		failed[s] = struct{}{}
	}
	hot, cold := d.splitHot(st, staged.Writes, step)
	d.hotWrites.Add(int64(len(hot)))
	d.coldWrites.Add(int64(len(cold)))
	began = time.Now()
	written, err := staged.Commit(ctx, cold)
	timing.commit = time.Since(began)
	if err != nil {
		return pipeline.DataResult{}, false, err
	}
	for subject, res := range written {
		if err := d.applyToWindow(ctx, st, subject, res); err != nil {
			if errors.Is(err, serieswindow.ErrSchemaChanged) {
				// 数据集字段变了：窗口要按新字段重建，重建完成之前该任务按预热处理。
				st.ready.Store(false)
				return pipeline.DataResult{Warming: subjects}, false, nil
			}
			return pipeline.DataResult{}, false, err
		}
	}
	// 热路径：每个标的只新增连续的 1 根，直接追加进窗口、用内存里的行组装，写盘留到计算期间并行进行。
	hotFrames := map[string]*storageio.Frame{}
	var flushList, fallback []seriesstore.SubjectWrite
	for _, w := range hot {
		if err := st.window.Append(w.Subject, w.Rows); err != nil {
			if errors.Is(err, serieswindow.ErrSchemaChanged) {
				st.ready.Store(false)
				return pipeline.DataResult{Warming: subjects}, false, nil
			}
			fallback = append(fallback, w) // 不连续等：退回同步写盘、从磁盘重载这个标的
			continue
		}
		hotFrames[w.Subject] = seriessync.ColumnsToFrame(w.Subject, w.Rows, plan.CarryColumns, st.types)
		flushList = append(flushList, w)
	}
	if len(fallback) > 0 {
		wrote, err := staged.Commit(ctx, fallback)
		if err != nil {
			return pipeline.DataResult{}, false, err
		}
		for subject := range wrote {
			if err := st.window.Reload(ctx, d.cfg.Store, subject); err != nil {
				return pipeline.DataResult{}, false, err
			}
		}
	}
	d.startFlush(ctx, st, staged, flushList)

	// observed 成员历史截面的标的只从窗口取本地历史，不参与上面的同步。窗口里缺了其中某个标的（窗口按旧的预载载入）时
	// 改读磁盘：Acquire 会静默跳过窗口外的标的，结果会少它。
	for _, subject := range plan.HistorySubjects {
		if !st.window.Has(subject) {
			log.Warnf("factor_history_subject_outside_window task=%s source=%s subject=%s：改读本地磁盘", plan.Set.TaskID, st.spec.Source, subject)
			return pipeline.DataResult{Failed: sortedKeys(failed)}, true, nil
		}
	}
	view := st.window.Acquire(withHistory(subjects, plan.HistorySubjects), target.Unix())
	for _, subject := range subjects {
		if end, has := st.window.Latest(subject); has && end.After(target) {
			return pipeline.DataResult{Failed: sortedKeys(failed)}, true, nil
		}
	}
	var ok []string
	for _, subject := range subjects {
		if _, bad := failed[subject]; bad {
			continue
		}
		// 同步之后本期没有真实源行的标的（数据还没到）按失败处理。
		if subjectMissingAt(view, subject, target.Unix(), true) {
			failed[subject] = struct{}{}
			continue
		}
		ok = append(ok, subject)
	}
	var onDisk []string
	for _, subject := range ok {
		if _, isHot := hotFrames[subject]; !isHot {
			onDisk = append(onDisk, subject)
		}
	}
	rows, err := d.rows(ctx, st, plan, onDisk, target, plan.TargetEnd)
	if err != nil {
		return pipeline.DataResult{}, false, err
	}
	for subject, frame := range hotFrames {
		rows[subject] = frame
	}
	return pipeline.DataResult{View: view, Frames: rows, Failed: sortedKeys(failed)}, false, nil
}

// subjectMissingAt 报告标的在视图里没有「目标时刻 target 的真实源行」：同一期一个标的至多一条序列，所以不看 tag，
// 只要有一条序列的最后一根恰好是目标期且不是填充行就不算缺。live 为假时（补算的区间）只看标的在视图里有没有数据。
func subjectMissingAt(view *serieswindow.View, subject string, target int64, live bool) bool {
	for _, sv := range view.Series(subject) {
		if sv.N == 0 {
			continue
		}
		if !live || (sv.End(view.Step) == target && !sv.Filled[sv.N-1]) {
			return false
		}
	}
	return true
}

// rows 读目标区间 [from, to) 的完整行（series_tag 与携带列），组装写出携带列用。
func (d *Data) rows(ctx context.Context, st *source, plan pipeline.Plan, subjects []string, from, to time.Time) (map[string]*storageio.Frame, error) {
	out := make(map[string]*storageio.Frame, len(subjects))
	if len(subjects) == 0 {
		return out, nil
	}
	got, err := d.cfg.Store.ReadRange(ctx, st.spec.Source, subjects, from, to)
	if err != nil {
		return nil, err
	}
	for _, subject := range subjects {
		if cols := got[subject]; cols != nil {
			out[subject] = seriessync.ColumnsToFrame(subject, cols, plan.CarryColumns, st.types)
		}
	}
	return out, nil
}

// loadRange 读本地存储的 [回看起点, TargetEnd)：补算与只填充都走这里，不碰 Storage。history 是 observed 成员历史截面的标的：
// 一起读进视图，但不判失败、不产生目标区间的行。
func (d *Data) loadRange(ctx context.Context, st *source, plan pipeline.Plan, subjects, history []string) (pipeline.DataResult, error) {
	if st == nil {
		return pipeline.DataResult{Failed: subjects}, nil
	}
	st.awaitFlush() // 补算、追赶读磁盘：先等最近一期的后台落盘结束
	st.mu.Lock()
	spec, types, numeric := st.spec, st.types, st.numeric
	st.mu.Unlock()
	if len(spec.Fields) == 0 {
		return pipeline.DataResult{Failed: subjects}, nil
	}
	step, err := seriesstore.Step(plan.Set.Freq)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	from, err := pipeline.ReadStart(periodclock.Continuous{}, plan)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	got, err := d.cfg.Store.ReadRange(ctx, spec.Source, withHistory(subjects, history), from, plan.TargetEnd)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	var failed []string
	for _, subject := range subjects {
		if got[subject] == nil {
			failed = append(failed, subject)
		}
	}
	view, err := serieswindow.FromColumns(spec.Source, int64(step/time.Second), numeric, got)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	rows := make(map[string]*storageio.Frame, len(subjects))
	for _, subject := range subjects {
		cols := got[subject]
		if cols == nil {
			continue
		}
		// 目标区间的行：补算的目标区间，或实时周期的最后一根。
		target := seriesstore.NewColumns(cols.Fields, 0)
		lo := plan.TargetStart.Unix()
		if plan.Mode == pipeline.ModeLive {
			// 实时周期（追赶读磁盘）的 TargetStart 是回看起点：行式的目标区间只取最后一根，不把整个回看窗口转成行。
			lo = plan.TargetEnd.Add(-time.Duration(step)).Unix()
		}
		for i := 0; i < cols.Len(); i++ {
			if cols.Times[i] >= lo && cols.Times[i] < plan.TargetEnd.Unix() {
				target.AppendRowFrom(cols, i)
			}
		}
		rows[subject] = seriessync.ColumnsToFrame(subject, target, plan.CarryColumns, types)
	}
	// 实时周期（追赶读磁盘）按目标那一根判断；补算与只填充的区间只看标的有没有数据。
	live, target := plan.Mode == pipeline.ModeLive, plan.TargetEnd.Add(-step).Unix()
	for _, subject := range subjects {
		if got[subject] != nil && subjectMissingAt(view, subject, target, live) {
			failed = append(failed, subject)
		}
	}
	return pipeline.DataResult{View: view, Frames: rows, Failed: failed}, nil
}
