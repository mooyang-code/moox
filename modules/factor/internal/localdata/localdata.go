// Package localdata 是因子管线的数据层（pipeline.Data 的实现）：把本地时序存储、内存窗口与同步器组合起来。
// 实时周期先把本期标的从 Storage 同步到本地（只拉增量），再从内存窗口取读租约；补算直接读本地存储。
// 管线自己不再从 Storage 读整窗口。
package localdata

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/seriessync"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// Storage 是数据层用到的 Storage 能力（storageio.Client 实现）。
type Storage interface {
	seriessync.Storage
	ListColumns(ctx context.Context, spaceID, datasetID string) ([]storageio.ColumnInfo, error)
}

// Config 是数据层的参数。
type Config struct {
	Store   *seriesstore.Store
	Storage Storage
	Clock   periodclock.Clock
	// Admit 非空时，窗口的常驻内存向它登记，新增窗口超出预算时拒绝（设计 4.2）。
	Admit *memadmit.Admitter
	// Workers 与 BatchSubjects 控制同步时并行读取 Storage 的批数与每批标的数。
	Workers       int
	BatchSubjects int
	// Headroom 是窗口数组余量的根数；0 取默认。
	Headroom int
	// WindowMax 是所有数据源窗口常驻内存的上限；新启用的集合会超出时拒绝启用。0 表示不限制。
	WindowMax int64
	Now       func() time.Time
}

// ErrChangeGap 表示数据源的变更日志同步位点断档（离线太久、Storage 节点更换）：要先执行对账。
var ErrChangeGap = errors.New("变更日志同步位点断档，需要执行对账")

// Data 是数据层。
type Data struct {
	cfg    Config
	syncer *seriessync.Syncer

	mu      sync.Mutex
	sources map[seriesstore.Source]*source
}

type source struct {
	setIDv atomic.Value // string：集合 ID，Prepare 时写入

	// mu 串行同一个数据源的同步、跟进变更与对账。
	mu      sync.Mutex
	set     domain.FactorSet
	spec    seriessync.Spec
	types   map[string]string
	numeric []string
	window  *serieswindow.Window
	blocked error

	// ready、expected、win 在不持有 mu 时也会被读取（实时周期判断要不要等、状态上报），所以用原子量；
	// 准备窗口、跟进变更这类长操作持有 mu 期间，实时周期看到 ready 为假就直接按预热处理，不会被它挡住。
	ready    atomic.Bool
	expected atomic.Int64
	win      atomic.Pointer[serieswindow.Window]
}

// New 创建数据层。
func New(cfg Config) *Data {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Data{
		cfg:     cfg,
		syncer:  &seriessync.Syncer{Store: cfg.Store, Storage: cfg.Storage, Workers: cfg.Workers, BatchSubjects: cfg.BatchSubjects},
		sources: map[seriesstore.Source]*source{},
	}
}

// Store 返回本地时序存储。
func (d *Data) Store() *seriesstore.Store { return d.cfg.Store }

// Syncer 返回同步器（对账命令用）。
func (d *Data) Syncer() *seriessync.Syncer { return d.syncer }

func sourceOf(set domain.FactorSet) seriesstore.Source {
	return seriesstore.Source{SpaceID: set.SpaceID, DatasetID: set.SourceDatasetID, Freq: set.Freq}
}

func (d *Data) lookup(src seriesstore.Source) *source {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sources[src]
}

func (st *source) setID() string {
	id, _ := st.setIDv.Load().(string)
	return id
}

func maxLookback(factors []domain.FactorDef) int {
	n := 1
	for _, f := range factors {
		n = max(n, f.LookbackPeriods)
	}
	return n
}

// ---------------------------------------------------------------- 准备

// Prepare 为一个集合准备数据：读数据集字段 → 取同步基线或跟进变更 → 把本地补到最新已收盘的一根 → 载入内存窗口。
// 可以重复调用（目录变化、集合新启用时）；窗口载入完成之前该集合的实时周期按预热处理（本期不算、记账本）。
// 窗口内存会超出进程预算时拒绝，并给出原因。
func (d *Data) Prepare(ctx context.Context, set domain.EngineSet, subjects []string) error {
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
	lookback := maxLookback(set.Factors)
	numeric := serieswindow.NumericFields(fields)

	d.mu.Lock()
	st := d.sources[src]
	if st == nil {
		st = &source{}
		d.sources[src] = st
	}
	d.mu.Unlock()

	st.mu.Lock()
	defer st.mu.Unlock()
	st.set = set.Set
	st.setIDv.Store(set.Set.SetID)
	st.spec = seriessync.Spec{Source: src, Fields: fields, Lookback: lookback}
	st.types = seriessync.ValueTypes(columns)
	st.numeric = numeric
	st.expected.Store(int64(len(subjects)))

	// 窗口内存超出上限或进程预算：拒绝启用并给出明确原因，不让窗口把计算挤出内存。
	need := serieswindow.BytesPerSeries(lookback, d.cfg.Headroom, len(numeric)) * int64(max(len(subjects), 1))
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
	end := d.cfg.Now().UTC().Truncate(step)
	if aligned, err := d.cfg.Clock.Align(end, set.Set.Freq); err == nil {
		end = aligned
	}
	pull, err := d.syncer.Pull(ctx, st.spec, subjects, end)
	if err != nil {
		return fmt.Errorf("同步本地数据：%w", err)
	}
	if len(pull.Failed) > 0 && len(pull.Failed) == len(subjects) {
		return fmt.Errorf("%w：所有标的同步失败", storageio.ErrInfra)
	}

	window := st.window
	if window == nil || window.Lookback() != lookback || !sameStrings(window.Fields(), numeric) {
		window, err = serieswindow.New(serieswindow.Config{Source: src, Lookback: lookback, Headroom: d.cfg.Headroom}, int64(step/time.Second), numeric)
		if err != nil {
			return err
		}
	}
	local, err := d.cfg.Store.Subjects(src)
	if err != nil {
		return err
	}
	want := intersect(subjects, local)
	if err := window.Load(ctx, d.cfg.Store, want); err != nil {
		return fmt.Errorf("载入内存窗口：%w", err)
	}
	st.window = window
	st.win.Store(window)
	st.ready.Store(true)
	if d.cfg.Admit != nil {
		d.cfg.Admit.SetResident("window:"+src.String(), window.Stats().Bytes)
	}
	return nil
}

// windowBytesExcept 是其他数据源窗口的实际占用。
func (d *Data) windowBytesExcept(except seriesstore.Source) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	var total int64
	for src, st := range d.sources {
		if src == except {
			continue
		}
		if w := st.win.Load(); w != nil {
			total += w.Stats().Bytes
		}
	}
	return total
}

// SourceInfo 是一个已准备的数据源及其集合。
type SourceInfo struct {
	Source seriesstore.Source
	SetID  string
}

// Sources 返回已准备（至少调用过 Prepare）的数据源。
func (d *Data) Sources() []SourceInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]SourceInfo, 0, len(d.sources))
	for src, st := range d.sources {
		if id := st.setID(); id != "" {
			out = append(out, SourceInfo{Source: src, SetID: id})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SetID < out[j].SetID })
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func intersect(want, have []string) []string {
	set := make(map[string]struct{}, len(have))
	for _, s := range have {
		set[s] = struct{}{}
	}
	var out []string
	for _, s := range want {
		if _, ok := set[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------- 管线接口

// Load 实现 pipeline.Data。
func (d *Data) Load(ctx context.Context, plan pipeline.Plan) (pipeline.DataResult, error) {
	src := sourceOf(plan.Set)
	st := d.lookup(src)
	subjects := append([]string(nil), plan.Available...)
	sort.Strings(subjects)
	if plan.Mode == pipeline.ModeLive {
		return d.loadLive(ctx, st, plan, subjects)
	}
	return d.loadRange(ctx, st, plan, subjects)
}

func (d *Data) loadLive(ctx context.Context, st *source, plan pipeline.Plan, subjects []string) (pipeline.DataResult, error) {
	// 窗口还没载入完成（或正在重建）：不等，本期按预热处理。
	if st == nil || !st.ready.Load() {
		return pipeline.DataResult{Warming: subjects}, nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.ready.Load() || st.window == nil {
		return pipeline.DataResult{Warming: subjects}, nil
	}
	step, err := seriesstore.Step(plan.Set.Freq)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	target := plan.TargetEnd.Add(-step)

	pull, err := d.syncer.Pull(ctx, st.spec, subjects, plan.TargetEnd)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	failed := map[string]struct{}{}
	for _, s := range pull.Failed {
		failed[s] = struct{}{}
	}
	for subject, res := range pull.Written {
		if err := d.applyToWindow(ctx, st, subject, res); err != nil {
			if errors.Is(err, serieswindow.ErrSchemaChanged) {
				// 数据集字段变了：窗口要按新字段重建，重建完成之前该集合按预热处理。
				st.ready.Store(false)
				return pipeline.DataResult{Warming: subjects}, nil
			}
			return pipeline.DataResult{}, err
		}
	}

	view := st.window.Acquire(subjects, target.Unix())
	var ok []string
	for _, subject := range subjects {
		if _, bad := failed[subject]; bad {
			continue
		}
		// 同步之后本地仍没有目标周期的行：数据还没到，本期按失败处理。
		if end, has := view.Latest(subject); !has || end < target.Unix() {
			failed[subject] = struct{}{}
			continue
		}
		ok = append(ok, subject)
	}
	rows, err := d.rows(ctx, st, plan, ok, target, plan.TargetEnd)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	return pipeline.DataResult{View: view, Frames: rows, Failed: sortedKeys(failed)}, nil
}

// applyToWindow 把一次同步写入的结果反映到窗口：新标的或修正了窗口里的区间就整个标的重新载入，否则追加新行。
func (d *Data) applyToWindow(ctx context.Context, st *source, subject string, res seriesstore.WriteResult) error {
	if res.Changed.Empty() {
		return nil
	}
	if res.PrevLatest.IsZero() || !st.window.Has(subject) || !res.Changed.From.After(res.PrevLatest) {
		return st.window.Reload(ctx, d.cfg.Store, subject)
	}
	got, err := d.cfg.Store.ReadRange(ctx, st.spec.Source, []string{subject}, res.PrevLatest.Add(time.Second), res.Latest.Add(time.Second))
	if err != nil {
		return err
	}
	rows := got[subject]
	if rows == nil {
		return nil
	}
	if err := st.window.Append(subject, rows); err != nil {
		if errors.Is(err, serieswindow.ErrDiscontinuous) {
			return st.window.Reload(ctx, d.cfg.Store, subject)
		}
		return err
	}
	return nil
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

// loadRange 读本地存储的 [回看起点, TargetEnd)：补算与只填充都走这里，不碰 Storage。
func (d *Data) loadRange(ctx context.Context, st *source, plan pipeline.Plan, subjects []string) (pipeline.DataResult, error) {
	if st == nil {
		return pipeline.DataResult{Failed: subjects}, nil
	}
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
	from, err := pipeline.ReadStart(d.cfg.Clock, plan)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	got, err := d.cfg.Store.ReadRange(ctx, spec.Source, subjects, from, plan.TargetEnd)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	var failed []string
	for _, subject := range subjects {
		if got[subject] == nil {
			failed = append(failed, subject)
		}
	}
	view, err := serieswindow.FromColumns(int64(step/time.Second), numeric, got)
	if err != nil {
		return pipeline.DataResult{}, err
	}
	rows := make(map[string]*storageio.Frame, len(got))
	for subject, cols := range got {
		// 目标区间的行：补算的目标区间，或实时周期的最后一根。
		target := seriesstore.NewColumns(cols.Fields, 0)
		lo := plan.TargetStart.Unix()
		for i := 0; i < cols.Len(); i++ {
			if cols.Times[i] >= lo && cols.Times[i] < plan.TargetEnd.Unix() {
				target.AppendRowFrom(cols, i)
			}
		}
		rows[subject] = seriessync.ColumnsToFrame(subject, target, plan.CarryColumns, types)
	}
	return pipeline.DataResult{View: view, Frames: rows, Failed: failed}, nil
}

// WindowStatuses 实现 pipeline.Data：各集合内存窗口的载入状态。
func (d *Data) WindowStatuses() []pipeline.WarmupStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []pipeline.WarmupStatus
	for _, st := range d.sources {
		if st.setID() == "" {
			continue
		}
		expected := int(st.expected.Load())
		status := pipeline.WarmupStatus{SetID: st.setID(), State: pipeline.WarmupWarming, ExpectedSubjects: expected}
		if window := st.win.Load(); window != nil {
			status.WarmSubjects = min(len(window.Subjects()), expected)
		}
		if st.ready.Load() {
			status.State = pipeline.WarmupReady
		}
		out = append(out, status)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SetID < out[j].SetID })
	return out
}

// ---------------------------------------------------------------- 跟进变更

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
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.spec.Source.DatasetID == "" || st.window == nil || st.blocked != nil && !errors.Is(st.blocked, ErrChangeGap) {
		return 0, false, nil
	}
	res, err := d.syncer.Follow(ctx, st.spec)
	if err != nil {
		return 0, false, err
	}
	if res.Gap {
		st.ready.Store(false)
		st.blocked = ErrChangeGap
		return 0, true, nil
	}
	for subject := range res.Applied {
		if err := st.window.Reload(ctx, d.cfg.Store, subject); err != nil {
			return 0, false, err
		}
	}
	return len(res.Applied), false, nil
}

// Reconcile 对账一个数据源：重建同步基线并把本地数据与 Storage 保留期内的数据比对；完成后窗口重新载入、数据源恢复就绪。
func (d *Data) Reconcile(ctx context.Context, src seriesstore.Source, subjects []string, progress func(done, total int)) (seriessync.ReconcileResult, error) {
	st := d.lookup(src)
	if st == nil {
		return seriessync.ReconcileResult{}, fmt.Errorf("数据源 %s 还没有准备好（集合未启用）", src)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	res, err := d.syncer.Reconcile(ctx, st.spec, subjects, progress)
	if err != nil {
		return res, err
	}
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

// ReconcileSet 在不启动窗口的情况下对账一个集合的数据源（引擎停止后由命令行执行，本地存储同一时刻只能由一个进程打开）。
func (d *Data) ReconcileSet(ctx context.Context, set domain.EngineSet, subjects []string, progress func(done, total int)) (seriessync.ReconcileResult, error) {
	src := sourceOf(set.Set)
	columns, err := d.cfg.Storage.ListColumns(ctx, src.SpaceID, src.DatasetID)
	if err != nil {
		return seriessync.ReconcileResult{}, fmt.Errorf("读取源数据集字段：%w", err)
	}
	spec := seriessync.Spec{Source: src, Fields: seriessync.FieldsFromColumns(columns), Lookback: maxLookback(set.Factors)}
	return d.syncer.Reconcile(ctx, spec, subjects, progress)
}

// Blocked 返回数据源当前不能计算的原因（没有时为 nil）。
func (d *Data) Blocked(src seriesstore.Source) error {
	st := d.lookup(src)
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.blocked
}

// WriteLocal 在数据源的串行锁内向本地存储写入（导入服务的本地写入入口）：写完把窗口里受影响的标的重新载入，
// 并把数据源还没有基线时先取好基线（之后 Storage 的变更才能被跟进）。数据源没有准备好时只写本地存储。
func (d *Data) WriteLocal(ctx context.Context, src seriesstore.Source, writes []seriesstore.SubjectWrite, opts seriesstore.WriteOptions) ([]seriesstore.WriteResult, error) {
	st := d.lookup(src)
	if st == nil {
		return d.cfg.Store.WriteBatch(ctx, src, writes, opts)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	results, err := d.cfg.Store.WriteBatch(ctx, src, writes, opts)
	if err != nil {
		return nil, err
	}
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

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// st 返回数据源状态（测试用）。
func (d *Data) st(src seriesstore.Source) *source { return d.lookup(src) }
