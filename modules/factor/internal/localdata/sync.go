package localdata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/seriessync"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

// FollowResult 是对所有数据源跟进变更的汇总。
type FollowResult struct {
	// Changed 是本次因 Storage 的修正而改变了本地数据的 (数据源, 标的) 数。
	Changed int
	// Gapped 是变更日志同步位点断档、需要对账的数据源。
	Gapped []seriesstore.Source
}

// Follow 对所有已准备的数据源读取 Storage 的行变更并应用到本地与窗口；失效重算待办随写入记进本地存储。
func (d *Data) Follow(ctx context.Context) (FollowResult, error) {
	d.mu.Lock()
	var srcs []*source
	for _, st := range d.sources {
		srcs = append(srcs, st)
	}
	d.mu.Unlock()
	var out FollowResult
	var firstErr error
	for _, st := range srcs {
		changed, gap, err := d.followSource(ctx, st)
		out.Changed += changed
		if gap {
			out.Gapped = append(out.Gapped, st.spec.Source)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return out, firstErr
}

func (d *Data) followSource(ctx context.Context, st *source) (int, bool, error) {
	d.lockSource(st, "变更跟进")
	defer st.mu.Unlock()
	if st.dropped {
		return 0, false, nil
	}
	d.settleFlush(st)
	if st.spec.Source.DatasetID == "" || st.window == nil || st.blocked != nil && !errors.Is(st.blocked, ErrChangeGap) {
		return 0, false, nil
	}
	started := time.Now()
	res, followErr := d.syncer.Follow(ctx, st.spec)
	// 跟进持有数据源锁，耗时与请求数要能从日志里看出来（设计 5.2「变更跟进的规则」第 4 条）。
	if stats := res.Stats; stats.Changes > 0 || followErr != nil || time.Since(started) > time.Second {
		log.Infof("factor_follow_done source=%s pages=%d changes=%d skipped=%d out_of_scope=%d refetched=%d replaced=%d requests=%d duration=%s error=%v",
			st.spec.Source, stats.Pages, stats.Changes, stats.Skipped, stats.OutOfScope, stats.Refetched, stats.Replaced, stats.Requests, time.Since(started).Round(time.Millisecond), followErr)
	}
	// 分页应用中途失败时，前面成功的页已经写进本地存储并推进了位点：这些标的的窗口也要重载，
	// 否则重试从新位点开始、不会再返回这些修正，窗口会一直留着旧值。
	for subject := range res.Applied {
		if err := st.window.Reload(ctx, d.cfg.Store, subject); err != nil {
			return 0, false, errors.Join(followErr, err)
		}
	}
	if followErr != nil {
		return len(res.Applied), false, followErr
	}
	if res.Gap {
		st.ready.Store(false)
		st.blocked = ErrChangeGap
		return 0, true, nil
	}
	return len(res.Applied), false, nil
}

// Reconcile 对账一个数据源：重建同步基线并把本地数据与 Storage 保留期内的数据比对；完成后窗口重新载入、数据源恢复就绪。
func (d *Data) Reconcile(ctx context.Context, src seriesstore.Source, subjects []string, progress func(done, total int)) (seriessync.ReconcileResult, error) {
	st := d.lookup(src)
	if st == nil {
		return seriessync.ReconcileResult{}, fmt.Errorf("数据源 %s 还没有准备好（任务未启用）", src)
	}
	d.lockSource(st, "对账")
	defer st.mu.Unlock()
	d.settleFlush(st)
	res, err := d.syncer.Reconcile(ctx, st.spec, subjects, progress)
	if err != nil {
		return res, err
	}
	d.syncer.ClearPulled(src)
	if st.window != nil {
		local, err := d.cfg.Store.Subjects(src)
		if err != nil {
			return res, err
		}
		if err := st.window.Load(ctx, d.cfg.Store, local); err != nil {
			return res, err
		}
		st.ready.Store(true)
		st.blocked = nil
	}
	return res, nil
}

// ReconcileTask 在不启动窗口的情况下对账一个任务的数据源（引擎停止后由命令行执行，本地存储同一时刻只能由一个进程打开）。
func (d *Data) ReconcileTask(ctx context.Context, set domain.EngineTask, subjects []string, progress func(done, total int)) (seriessync.ReconcileResult, error) {
	src := sourceOf(set.Set)
	columns, err := d.cfg.Storage.ListColumns(ctx, src.SpaceID, src.DatasetID)
	if err != nil {
		return seriessync.ReconcileResult{}, fmt.Errorf("读取源数据集字段：%w", err)
	}
	spec := seriessync.Spec{Source: src, Fields: seriessync.FieldsFromColumns(columns), Lookback: d.lookbackFor(src, set.Members)}
	return d.syncer.Reconcile(ctx, spec, subjects, progress)
}

// WriteLocal 在数据源的串行锁内向本地存储写入（导入服务的本地写入入口）：写完把窗口里受影响的标的重新载入，
// 并把数据源还没有基线时先取好基线（之后 Storage 的变更才能被跟进）。数据源没有准备好时只写本地存储。
func (d *Data) WriteLocal(ctx context.Context, src seriesstore.Source, writes []seriesstore.SubjectWrite, opts seriesstore.WriteOptions) ([]seriesstore.WriteResult, error) {
	st := d.lookup(src)
	if st == nil {
		return d.cfg.Store.WriteBatch(ctx, src, writes, opts)
	}
	d.lockSource(st, "本地写入")
	defer st.mu.Unlock()
	d.settleFlush(st)
	results, err := d.cfg.Store.WriteBatch(ctx, src, writes, opts)
	if err != nil {
		return nil, err
	}
	d.syncer.ClearPulled(src) // 本地数据被拉取之外的途径覆盖了：不能再用「拉取已反映」跳过变更
	if st.window != nil {
		for _, r := range results {
			if r.Changed.Empty() {
				continue
			}
			if err := st.window.Reload(ctx, d.cfg.Store, r.Subject); err != nil {
				return results, err
			}
		}
	}
	return results, nil
}

// BackfillTask 把任务源数据每个标的的本地历史从 Storage 补到最多 bars 根（只补本地最早一根之前、Storage 保留期内的部分）。
// 不碰窗口：由引擎停止时的命令行使用。subjects 为空表示本地有数据的全部标的。
func (d *Data) BackfillTask(ctx context.Context, set domain.EngineTask, subjects []string, bars int, progress func(done, total int)) (int, error) {
	src := sourceOf(set.Set)
	columns, err := d.cfg.Storage.ListColumns(ctx, src.SpaceID, src.DatasetID)
	if err != nil {
		return 0, fmt.Errorf("读取源数据集字段：%w", err)
	}
	spec := seriessync.Spec{Source: src, Fields: seriessync.FieldsFromColumns(columns), Lookback: d.lookbackFor(src, set.Members)}
	if len(subjects) == 0 {
		if subjects, err = d.cfg.Store.Subjects(src); err != nil {
			return 0, err
		}
	}
	written, err := d.syncer.Backfill(ctx, spec, subjects, bars, progress)
	return len(written), err
}

// CatchUp 把任务源数据的本地存储从 Storage 往后拉到 end（不含；不超过最新已收盘的一根），返回有新写入的标的数。
//
// 补算只读本地存储。Storage 里比本地最新一根更晚的数据平时留给实时计算去拉，但它不一定会来：强制补采写入的周期不发实时事件，
// 新建的任务也可能还没跑过实时周期。补算区间落在这段上时不先拉一次，就会读不到源数据、写出 0 行而作业仍然成功。
//
//   - 在数据源锁内拉（与变更跟进串行），新写入的行同时反映到窗口。没登记过的数据源不拉：补算本来就读不到它，
//     Prepare 自己会拉到最新。
//   - 只拉数据源上登记的标的范围内、本地已有数据的标的（subjects 为空表示这个范围内的全部标的）。本地没有数据的标的不拉：
//     第一次准备在取同步基线之前就失败时本地是空的，靠这一条不会在没有基线的情况下写入（那样会漏掉之后的修正）。
//   - 有标的读取失败时返回错误（已经拉到的照常写入）。
func (d *Data) CatchUp(ctx context.Context, set domain.EngineTask, subjects []string, end time.Time) (int, error) {
	src := sourceOf(set.Set)
	limit, err := d.axisNow(src)
	if err != nil {
		return 0, err
	}
	if end.After(limit) {
		end = limit
	}
	st := d.lookup(src)
	if st == nil {
		return 0, nil
	}
	d.lockSource(st, "补算前同步")
	defer st.mu.Unlock()
	// Prepare 刚登记数据源、还没写入同步规格时可能先被这里拿到锁。
	if st.spec.Source.DatasetID == "" {
		return 0, nil
	}
	// 限定在数据源上登记的标的范围内：范围之外的标的不在变更跟进里，拉进来也不会再被修正。
	if len(subjects) == 0 {
		subjects = st.subjects
	} else {
		subjects = intersect(subjects, st.subjects)
	}
	local, err := d.cfg.Store.Subjects(src)
	if err != nil {
		return 0, err
	}
	if subjects = intersect(subjects, local); len(subjects) == 0 {
		return 0, nil
	}
	// 上一期的热路径写盘必须先落完：拉取按本地最新一根决定从哪里拉。
	d.settleFlush(st)
	staged, err := d.syncer.PullStaged(ctx, st.spec, subjects, end)
	if err != nil {
		return 0, err
	}
	written, err := staged.Commit(ctx, staged.Writes)
	if err != nil {
		return 0, err
	}
	if st.window != nil {
		for subject, res := range written {
			if err := d.syncWindow(ctx, st, subject, res); err != nil {
				return len(written), err
			}
		}
	}
	if len(staged.Failed) > 0 {
		return len(written), fmt.Errorf("%w：%d 个标的拉取失败", storageio.ErrInfra, len(staged.Failed))
	}
	return len(written), nil
}

// RealRowsAt 判断没有事件可用的周期（连续水位追赶）哪些标的这一期已经到了：先把 subjects 从 Storage 增量同步到本地
// （拉到 period 这一根为止；与实时路径一样，新进名单、本地还没有数据的标的也同步，从回看根数之前拉起），
// 再看每个标的在本地这一期有没有真实的行（口径见 serieswindow.SeriesView.RealRowAt：有行、不是填充行、至少一个数值字段非 NaN）。
// 不按本地最新时间判断：中间缺一根、后面已经到了的标的，这一期仍算没到。返回这一期有真实行的标的。
// 数据源还没准备好（窗口没载入）或有标的拉取失败时返回 ErrInfra：判断不可靠，由调用方稍后重试。
func (d *Data) RealRowsAt(ctx context.Context, set domain.FactorTask, subjects []string, period time.Time) (map[string]bool, error) {
	src := sourceOf(set)
	st := d.lookup(src)
	if st == nil || !st.ready.Load() {
		return nil, fmt.Errorf("%w：数据源 %s 的本地窗口还没载入", storageio.ErrInfra, src)
	}
	step, err := seriesstore.Step(src.Freq)
	if err != nil {
		return nil, err
	}
	d.lockSource(st, "连续水位判断")
	defer st.mu.Unlock()
	if !st.ready.Load() || st.window == nil {
		return nil, fmt.Errorf("%w：数据源 %s 的本地窗口还没载入", storageio.ErrInfra, src)
	}
	d.settleFlush(st)
	end := period.Add(step)
	staged, err := d.syncer.PullStaged(ctx, st.spec, subjects, end)
	if err != nil {
		return nil, err
	}
	written, err := staged.Commit(ctx, staged.Writes)
	if err != nil {
		return nil, err
	}
	for subject, res := range written {
		if err := d.syncWindow(ctx, st, subject, res); err != nil {
			return nil, err
		}
	}
	if len(staged.Failed) > 0 {
		return nil, fmt.Errorf("%w：%d 个标的拉取失败", storageio.ErrInfra, len(staged.Failed))
	}
	got, err := d.cfg.Store.ReadRange(ctx, src, subjects, period, end)
	if err != nil {
		return nil, err
	}
	view, err := serieswindow.FromColumns(src, int64(step/time.Second), st.numeric, got)
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(subjects))
	for _, subject := range subjects {
		for _, sv := range view.Series(subject) {
			if sv.RealRowAt(view.Step, period.Unix()) {
				present[subject] = true
				break
			}
		}
	}
	return present, nil
}
