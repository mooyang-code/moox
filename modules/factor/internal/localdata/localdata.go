// Package localdata 是因子管线的数据层（pipeline.Data 的实现）：把本地时序存储、内存窗口与同步器组合起来。
// 实时周期先把本期标的从 Storage 同步到本地（只拉增量），再从内存窗口取读租约；补算直接读本地存储。
// 管线自己不再从 Storage 读整窗口。
package localdata

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
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
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"trpc.group/trpc-go/trpc-go/log"
)

// Storage 是数据层用到的 Storage 能力（storageio.Client 实现）。
type Storage interface {
	seriessync.Storage
	ListColumns(ctx context.Context, spaceID, datasetID string) ([]storageio.ColumnInfo, error)
	// GetDataset 取数据集信息，其中带生效的保留期（回看数量 N 能否从 Storage 补齐、新上市豁免都要用到它）。
	GetDataset(ctx context.Context, spaceID, datasetID string) (storageio.DatasetInfo, error)
}

// Config 是数据层的参数。
type Config struct {
	// Synced 返回数据源上“正在同步”的标的：预载最近一次从 Storage 取到的数据集在册清单（引擎的 syncedScope）。observed 成员
	// 每期计算时不纳入其中、而本任务范围不含的标的（HistoryRule）；窗口载入不看它。nil 表示只看本任务的范围。
	Synced func(ctx context.Context, src seriesstore.Source) ([]string, error)
	Store  *seriesstore.Store
	// Storage 读写的都是轴上时间（storageio.Axis 包装过的客户端）：数据层内部全部在轴上，按 periodclock.Continuous 做周期数学。
	Storage Storage
	// Admit 非空时，窗口的常驻内存向它登记，新增窗口超出预算时拒绝（设计 4.2）。
	Admit *memadmit.Admitter
	// Workers 与 BatchSubjects 控制同步时并行读取 Storage 的批数与每批标的数。
	Workers       int
	BatchSubjects int
	// Headroom 是窗口数组余量的根数；0 取默认。
	Headroom int
	// Lookback 返回数据源的回看数量 N（设计 8.1，决定 23）：窗口长度、本地要保有的根数、因子回看的上限。
	// 为 nil 或返回非正数时取任务内各因子的最大回看。
	Lookback func(src seriesstore.Source) int
	// LockWaitWarn 是实时同步等数据源锁的告警阈值（设计 4.1），0 取 1 秒。
	LockWaitWarn time.Duration
	// WindowMax 是所有数据源窗口常驻内存的上限；新启用的任务会超出时拒绝启用。0 表示不限制。
	WindowMax int64
	Now       func() time.Time
}

// ErrChangeGap 表示数据源的变更日志同步位点断档（离线太久、Storage 节点更换）：要先执行对账。
var ErrChangeGap = errors.New("变更日志同步位点断档，需要执行对账")

// NoDataError 表示任务里有标的在本地和 Storage 都没有任何数据（设计 6.2，决定 24）：数据源未就绪，
// 要导入历史数据，或把这些标的从源数据集的标签里移除。
type NoDataError struct {
	Subjects []string
}

func (e *NoDataError) Error() string {
	shown := e.Subjects
	if len(shown) > 5 {
		shown = shown[:5]
	}
	return fmt.Sprintf("%d 个标的在本地和 Storage 都没有数据（例如 %s）：请导入历史数据，或把它们从源数据集的标签里移除", len(e.Subjects), strings.Join(shown, "、"))
}

// LookbackExceededError 表示任务里有因子的回看超过了数据源的回看数量 N。
type LookbackExceededError struct {
	N       int
	Factors []string
	Max     int
}

func (e *LookbackExceededError) Error() string {
	return fmt.Sprintf("因子 %s 的回看（最长 %d 根）超过数据源的回看数量 N=%d：请调大 engine.yaml 的回看数量（7×24 市场 window.lookback，有交易时段的市场 window.session_lookback，或按数据源的 window.lookback_overrides），或缩短因子的回看", strings.Join(e.Factors, "、"), e.Max, e.N)
}

// Data 是数据层。
type Data struct {
	cfg    Config
	syncer *seriessync.Syncer

	mu      sync.Mutex
	sources map[seriesstore.Source]*source

	// unified 缓存各数据源是不是统一数据集（数据集属性 market_type=multi），避免每个补算分块都读一次元数据。
	unifiedMu sync.Mutex
	unified   map[seriesstore.Source]unifiedEntry

	// hotWrites 是走热路径（窗口先追加、写盘并行）的标的写入累计数，coldWrites 是同步写盘的累计数。
	hotWrites, coldWrites atomic.Int64
}

type unifiedEntry struct {
	multi bool
	at    time.Time
}

// unifiedCacheTTL 是“是否统一数据集”结论的缓存时间；数据集的 market_type 创建后不会改变，缓存只为减少元数据读取。
const unifiedCacheTTL = time.Minute

type coverageKey struct {
	subject  string
	earliest int64
	lookback int
}

// setReg 是一个任务在数据源上的登记：它的标的范围与回看。同一数据源可以被多个任务（例如各策略实例的私有任务）共用，
// 数据源按全部登记任务的标的并集与最大回看准备。
type setReg struct {
	lookback int
	subjects []string
	// observed 为真表示任务里有成员写了 cross_section_history: observed：窗口还要载入本地存储里回看窗口内有数据、
	// 但不在任何任务标的范围里的标的（只读本地历史，不同步）。
	observed bool
}

type source struct {
	// mu 串行同一个数据源的同步、跟进变更与对账。
	mu sync.Mutex
	// regs 是登记在这个数据源上的任务（Prepare 时写入、ReleaseTask 时删除）；regsMu 让状态上报在不持有 mu 时也能读。
	regsMu  sync.Mutex
	regs    map[string]setReg
	spec    seriessync.Spec
	types   map[string]string
	numeric []string
	window  *serieswindow.Window
	blocked error
	// subjects 是 Prepare 时任务的标的全集；covered 缓存「回看根数够了或属于新标的」的结论，键是 标的、本地最早时间、回看根数。
	subjects []string
	covered  map[coverageKey]struct{}
	// flush 是上一期还没落盘的热路径写入（设计 5.2，决定 25）：窗口已经追加、计算已经开始，写盘与计算并行。
	// 下一次要用到磁盘或窗口一致性的操作之前必须先等它结束（settleFlush）。
	flushMu sync.Mutex
	flush   *flushJob

	// exemptNew 为真时，本地最早一根就是 Storage 首根的标的算新上市，根数不足 N 也通过覆盖检查（设计 6.2）：
	// 要求数据源是 7×24 市场，并且 N 不超过数据集保留期能容纳的根数。
	exemptNew bool

	// ready、win 在不持有 mu 时也会被读取（实时周期判断要不要等、状态上报），所以用原子量；
	// 准备窗口、跟进变更这类长操作持有 mu 期间，实时周期看到 ready 为假就直接按预热处理，不会被它挡住。
	ready atomic.Bool
	win   atomic.Pointer[serieswindow.Window]
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
		unified: map[seriesstore.Source]unifiedEntry{},
	}
}

// UnifiedDataset 报告数据源是不是统一数据集（同一数据集里放现货、永续两个市场的序列）：以数据集属性
// market_type=multi 为准（采集建数据集时写入），不靠本地存储里有哪些 series_tag 推断。
func (d *Data) UnifiedDataset(ctx context.Context, src seriesstore.Source) (bool, error) {
	d.unifiedMu.Lock()
	if entry, ok := d.unified[src]; ok && d.cfg.Now().Sub(entry.at) < unifiedCacheTTL {
		d.unifiedMu.Unlock()
		return entry.multi, nil
	}
	d.unifiedMu.Unlock()
	info, err := d.cfg.Storage.GetDataset(ctx, src.SpaceID, src.DatasetID)
	if err != nil {
		return false, fmt.Errorf("读取源数据集 %s/%s 的 market_type：%w", src.SpaceID, src.DatasetID, err)
	}
	multi := strings.EqualFold(strings.TrimSpace(info.Attributes["market_type"]), seriesset.MarketMulti)
	d.unifiedMu.Lock()
	d.unified[src] = unifiedEntry{multi: multi, at: d.cfg.Now()}
	d.unifiedMu.Unlock()
	return multi, nil
}

// Reset 丢弃内存里的全部数据源（窗口、同步状态）并释放常驻内存登记（重置时用，之后要对每个任务重新 Prepare）。
func (d *Data) Reset() {
	d.Drain() // 热路径的后台落盘先写完：之后本地存储要被清空，不能让它在清空之后再写进去
	d.mu.Lock()
	sources := d.sources
	d.sources = map[seriesstore.Source]*source{}
	d.mu.Unlock()
	for src, st := range sources {
		st.ready.Store(false)
		st.win.Store(nil)
		if d.cfg.Admit != nil {
			d.cfg.Admit.SetResident("window:"+src.String(), 0)
		}
	}
}

// Store 返回本地时序存储。
func (d *Data) Store() *seriesstore.Store { return d.cfg.Store }

// Syncer 返回同步器（对账命令用）。
func (d *Data) Syncer() *seriessync.Syncer { return d.syncer }

func sourceOf(set domain.FactorTask) seriesstore.Source {
	return seriesstore.Source{SpaceID: set.SpaceID, DatasetID: set.SourceDatasetID, Freq: set.Freq}
}

func (d *Data) lookup(src seriesstore.Source) *source {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sources[src]
}

// registered 返回登记在数据源上的任务（按任务 ID 排序）及其登记。
func (st *source) registered() ([]string, map[string]setReg) {
	st.regsMu.Lock()
	defer st.regsMu.Unlock()
	ids := make([]string, 0, len(st.regs))
	regs := make(map[string]setReg, len(st.regs))
	for id, reg := range st.regs {
		ids = append(ids, id)
		regs[id] = reg
	}
	sort.Strings(ids)
	return ids, regs
}

func maxLookback(members []domain.MemberDef) int {
	n := 1
	for _, member := range members {
		n = max(n, member.Factor.LookbackPeriods)
	}
	return n
}

// lookbackFor 返回数据源的回看数量 N（决定 23）；没有配置时取成员的最大回看。
func (d *Data) lookbackFor(src seriesstore.Source, members []domain.MemberDef) int {
	if d.cfg.Lookback != nil {
		if n := d.cfg.Lookback(src); n > 0 {
			return n
		}
	}
	return maxLookback(members)
}

// ReleaseTask 取消任务在数据源上的登记（任务离开目录、改绑到别的数据源时）。数据源还有其他任务登记时，窗口下次准备时
// 按其余任务的范围与回看收缩；没有任何登记时直接丢弃数据源的窗口与同步状态，并释放它的常驻内存预算。
// 等数据源锁与上一期的后台落盘结束再释放，不会打断在途的同步、跟进或落盘。
func (d *Data) ReleaseTask(set domain.FactorTask) {
	src := sourceOf(set)
	st := d.lookup(src)
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	d.settleFlush(st)
	st.regsMu.Lock()
	delete(st.regs, set.TaskID)
	empty := len(st.regs) == 0
	st.regsMu.Unlock()
	if !empty {
		return
	}
	d.mu.Lock()
	if d.sources[src] == st {
		delete(d.sources, src)
	}
	d.mu.Unlock()
	st.ready.Store(false)
	st.win.Store(nil)
	if d.cfg.Admit != nil {
		d.cfg.Admit.SetResident("window:"+src.String(), 0)
	}
}

// lockSource 取数据源锁并度量等待时间：实时同步等锁超过阈值（默认 1 秒）说明导入、跟进或对账占着锁，记告警（设计 4.1）。
func (d *Data) lockSource(st *source, op string) {
	started := time.Now()
	st.mu.Lock()
	warn := d.cfg.LockWaitWarn
	if warn <= 0 {
		warn = time.Second
	}
	if waited := time.Since(started); waited > warn {
		log.Warnf("factor_source_lock_wait source=%s op=%s waited=%s", st.spec.Source, op, waited.Round(time.Millisecond))
	}
}

// ---------------------------------------------------------------- 准备

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
	numeric := serieswindow.NumericFields(fields)

	d.mu.Lock()
	st := d.sources[src]
	if st == nil {
		st = &source{regs: map[string]setReg{}}
		d.sources[src] = st
	}
	d.mu.Unlock()

	st.mu.Lock()
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

	// 因子的回看都不能超过回看数量 N（设计 8.1）。
	if over := factorsOverLookback(set.Members, lookback); over != nil {
		st.ready.Store(false)
		st.blocked = over
		return over
	}
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
	// 本地和 Storage 都没有任何数据的标的：拉取成功返回了空结果（拉取失败的不算，那只是同步失败）。数据源未就绪，要导入或移除。
	if noData := noDataSubjects(subjects, local, pull.Failed); len(noData) > 0 {
		st.ready.Store(false)
		st.blocked = &NoDataError{Subjects: noData}
		return st.blocked
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

// axisNow 是当前时刻在数据源轴上的排他终点：最新已收盘一根的下一期。
func (d *Data) axisNow(src seriesstore.Source) (time.Time, error) {
	axis, err := periodclock.AxisFor(src.SpaceID, src.Freq)
	if err != nil {
		return time.Time{}, err
	}
	return axis.AxisEnd(d.cfg.Now())
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

// SourceInfo 是一个已准备的数据源及其任务。
type SourceInfo struct {
	Source seriesstore.Source
	TaskID string
}

// Sources 返回已准备（至少调用过 Prepare）的数据源。
func (d *Data) Sources() []SourceInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]SourceInfo, 0, len(d.sources))
	for src, st := range d.sources {
		ids, _ := st.registered()
		for _, id := range ids {
			out = append(out, SourceInfo{Source: src, TaskID: id})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
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
	return d.loadRange(ctx, st, plan, subjects, plan.HistorySubjects)
}

// DelistedMinSilence 是判断序列已下架的最短沉默时长：序列的最后一行比数据源最新一根早过这么久（见 DelistedThreshold）才可能是已下架。
// 它是独立的常量，与跳过恢复的 give_up_after 无关：调小 give_up_after 不会让晚到的序列被当成下架。
const DelistedMinSilence = 6 * time.Hour

// DelistedThreshold 是已下架的沉默阈值：max(DelistedMinSilence, k×周期)，k 为 2；步长不小于 8 小时（日线等）时按 3 期，
// 与 LongGapThreshold 同一口径。周期长于几小时的任务，晚到一根的序列沉默时长就是一两个周期，不能因为超过固定时长就当成下架。
func DelistedThreshold(step time.Duration) time.Duration {
	k := time.Duration(2)
	if step >= 8*time.Hour {
		k = 3
	}
	return max(DelistedMinSilence, k*step)
}

// seriesDelisted 判断一条序列在本次计划里是否已下架：最后一行早于计划区间起点（区间内断档、延续到区间里的序列不算），
// 并且相对参照时刻（数据源最新一根）已经沉默了超过阈值。沉默时长恰等于阈值仍算应存在。
func seriesDelisted(latest, targetStart, reference time.Time, step time.Duration) bool {
	return latest.Before(targetStart) && reference.Sub(latest) > DelistedThreshold(step)
}

// UnifiedTask 报告任务的源数据集是不是统一数据集（数据集属性 market_type=multi）。
func (d *Data) UnifiedTask(ctx context.Context, set domain.FactorTask) (bool, error) {
	return d.UnifiedDataset(ctx, sourceOf(set))
}

// ExpectedSeries 实现 pipeline.SeriesExpecter：补算类计划在统一数据集（数据集属性 market_type=multi）上的期望序列，
// 取本地存储里这些标的“应存在”的每条序列（标的、series_tag）。这是没有冻结全集可用时的兜底（人工补算、失效重算、
// 没有 marker 的待办）；能读到周期 marker 的恢复路径直接用冻结的 universe_series，不走这里。
// 应存在 = 计划区间结束之前已有数据，并且没有下架（见 seriesDelisted）：下架判断的参照点是“数据源最新一根已经沉默多久”，
// 不是区间起点——历史重算块能正确排除早已下架的合约，而最近一期晚到的永续（沉默时长只有一两个周期）仍算应存在；
// 阈值与回看根数、give_up_after 都无关：回看只决定读多远的窗口，分不清“晚到停更”和“已下架”。
// 兜底推导是推测：刚下架不久（沉默不到阈值）的合约分不出来，仍会被当成应存在，所以补算类计划里因此缺数而被跳过的 tag
// 不写 NULL（见 Plan.ExpectedSeriesDerived）。单序列数据集返回 nil。
func (d *Data) ExpectedSeries(ctx context.Context, plan pipeline.Plan) ([]seriesset.Series, error) {
	src := sourceOf(plan.Set)
	multi, err := d.UnifiedDataset(ctx, src)
	if err != nil || !multi {
		return nil, err
	}
	step, err := seriesstore.Step(src.Freq)
	if err != nil {
		return nil, err
	}
	var all []seriesBounds
	for _, subject := range plan.Expected {
		bounds, err := d.cfg.Store.TagBounds(src, subject)
		if err != nil {
			return nil, err
		}
		for tag, b := range bounds {
			all = append(all, seriesBounds{subject: subject, tag: tag, bounds: b})
		}
	}
	return expectedFromBounds(all, plan.TargetStart, plan.TargetEnd, step), nil
}

// seriesBounds 是本地存储里一条序列（标的、series_tag）的数据边界。
type seriesBounds struct {
	subject, tag string
	bounds       seriesstore.Bounds
}

// expectedFromBounds 从各序列的本地边界选出应存在的序列。参照时刻是它们里最新的一根（数据源最新一根）。
func expectedFromBounds(all []seriesBounds, targetStart, targetEnd time.Time, step time.Duration) []seriesset.Series {
	var reference time.Time
	for _, item := range all {
		if item.bounds.Latest.After(reference) {
			reference = item.bounds.Latest
		}
	}
	end := targetEnd.Unix()
	var out []seriesset.Series
	for _, item := range all {
		if item.bounds.Earliest.Unix() >= end || seriesDelisted(item.bounds.Latest, targetStart, reference, step) {
			continue
		}
		out = append(out, seriesset.Series{SubjectID: item.subject, SeriesTag: item.tag})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubjectID != out[j].SubjectID {
			return out[i].SubjectID < out[j].SubjectID
		}
		return out[i].SeriesTag < out[j].SeriesTag
	})
	return out
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
	// 统一数据集按各序列自己的进度拉取：落后的市场（例如永续晚到）不会被另一个市场的最新时间盖住。
	staged, err := d.syncer.PullStagedTags(ctx, st.spec, subjects, plan.TagsBySubject(), plan.TargetEnd)
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
	var failedSeries []seriesset.Series
	tagsOf := plan.TagsBySubject()
	for _, subject := range subjects {
		if _, bad := failed[subject]; bad {
			continue
		}
		if plan.Multi() {
			// 统一数据集：每条期望序列各自判断本期有没有真实的源行。有些序列还没到时，到了的序列照常算；
			// 一条都没到的标的整体按失败处理。
			missing := missingTagsAt(view, subject, tagsOf[subject], target.Unix(), true)
			if len(missing) == len(tagsOf[subject]) {
				failed[subject] = struct{}{}
				continue
			}
			for _, tag := range missing {
				failedSeries = append(failedSeries, seriesset.Series{SubjectID: subject, SeriesTag: tag})
			}
			ok = append(ok, subject)
			continue
		}
		// 同步之后本地仍没有目标周期的行：数据还没到，本期按失败处理。
		if end, has := view.Latest(subject); !has || end < target.Unix() {
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
	return pipeline.DataResult{View: view, Frames: rows, Failed: sortedKeys(failed), FailedSeries: failedSeries}, false, nil
}

// missingTagsAt 返回这个标的在视图里没有「目标时刻 target 的真实源行」的 series_tag：序列不在视图里、最后一根早于目标、
// 或目标那一根是填充行。live 为假时（补算的区间）只看序列有没有数据。
func missingTagsAt(view *serieswindow.View, subject string, tags []string, target int64, live bool) []string {
	have := make(map[string]serieswindow.SeriesView, len(tags))
	for _, sv := range view.Series(subject) {
		have[sv.Tag] = sv
	}
	var missing []string
	for _, tag := range tags {
		sv, ok := have[tag]
		switch {
		case !ok || sv.N == 0:
			missing = append(missing, tag)
		case live && (sv.End(view.Step) != target || sv.Filled[sv.N-1]):
			missing = append(missing, tag)
		}
	}
	return missing
}

// flushJob 是一次后台落盘。
type flushJob struct {
	done     chan struct{}
	subjects []string
	err      error
	// staged 与 writes 留着，落盘失败时下一次操作先原样重试一次。
	staged *seriessync.StagedPull
	writes []seriesstore.SubjectWrite
}

// flushTimeout 是一次后台落盘的上限。
const flushTimeout = 2 * time.Minute

// splitHot 把拉取到的写入分成热路径与其余：热路径是本地已有的标的恰好新增连续的 1 根（不需要填充缺失行），窗口也正好停在本地 Latest。
// 其余（新标的、缺期、一次拉到多根）保持同步写盘，因为窗口要按磁盘上生成的填充行重载。
func (d *Data) splitHot(st *source, writes []seriesstore.SubjectWrite, step time.Duration) (hot, cold []seriesstore.SubjectWrite) {
	for _, w := range writes {
		if w.Rows.Len() > 1 {
			// 统一数据集：一个标的同一期有多条序列的新行，每条序列恰好新增连续的 1 根时也走热路径。
			if d.hotSeriesWrite(st, w, step) {
				hot = append(hot, w)
			} else {
				cold = append(cold, w)
			}
			continue
		}
		if w.Rows.Len() != 1 || !st.window.Has(w.Subject) {
			cold = append(cold, w)
			continue
		}
		b, ok, err := d.cfg.Store.Bounds(st.spec.Source, w.Subject)
		latest, inWindow := st.window.Latest(w.Subject)
		if err != nil || !ok || !inWindow || !latest.Equal(b.Latest) || !w.From.Equal(b.Latest.Add(step)) || w.Rows.Times[0] != w.From.Unix() {
			cold = append(cold, w)
			continue
		}
		hot = append(hot, w)
	}
	return hot, cold
}

// hotSeriesWrite 判断一次多行写入能否走热路径：每行属于不同的 series_tag，本地与窗口里这条序列的最新时间都正好是这一行的前一根。
// 单序列的判断见 splitHot；多序列各自判断，别的序列没有新行（还没到）不影响。
func (d *Data) hotSeriesWrite(st *source, w seriesstore.SubjectWrite, step time.Duration) bool {
	if !st.window.Has(w.Subject) {
		return false
	}
	local, err := d.cfg.Store.TagBounds(st.spec.Source, w.Subject)
	if err != nil {
		return false
	}
	seen := make(map[string]struct{}, w.Rows.Len())
	for i := 0; i < w.Rows.Len(); i++ {
		tag := w.Rows.Tags[i]
		if _, dup := seen[tag]; dup {
			return false
		}
		seen[tag] = struct{}{}
		b, ok := local[tag]
		latest, inWindow := st.window.LatestOf(w.Subject, tag)
		if !ok || !inWindow || !latest.Equal(b.Latest) || w.Rows.Times[i] != b.Latest.Add(step).Unix() {
			return false
		}
	}
	return true
}

// startFlush 在后台写入热路径的行。调用时持有数据源锁；下一次需要磁盘一致的操作会先 settleFlush。
func (d *Data) startFlush(ctx context.Context, st *source, staged *seriessync.StagedPull, writes []seriesstore.SubjectWrite) {
	if len(writes) == 0 {
		return
	}
	job := &flushJob{done: make(chan struct{}), subjects: make([]string, len(writes)), staged: staged, writes: writes}
	for i, w := range writes {
		job.subjects[i] = w.Subject
	}
	st.flushMu.Lock()
	st.flush = job
	st.flushMu.Unlock()
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
	go func() {
		defer cancel()
		defer close(job.done)
		_, job.err = staged.Commit(flushCtx, writes)
	}()
}

// awaitFlush 等上一期的后台落盘结束（不清除记录），返回它的任务；没有则为 nil。
func (st *source) awaitFlush() *flushJob {
	st.flushMu.Lock()
	job := st.flush
	st.flushMu.Unlock()
	if job != nil {
		<-job.done
	}
	return job
}

// settleFlush 等上一期的后台落盘结束并清除记录；调用方持有数据源锁。落盘失败时窗口比磁盘多了几根：
// 把这些标的从磁盘重载（丢掉没落盘的行），下一次拉取会重新取回，记错误日志。
func (d *Data) settleFlush(st *source) {
	job := st.awaitFlush()
	if job == nil {
		return
	}
	st.flushMu.Lock()
	if st.flush == job {
		st.flush = nil
	}
	st.flushMu.Unlock()
	if job.err == nil {
		return
	}
	log.Errorf("factor_flush_failed source=%s subjects=%d error=%v", st.spec.Source, len(job.subjects), job.err)
	// 先原样重试一次：窗口里的行已经用来计算并发布了，能写成功就保持本地与已发布的结果一致，
	// 之后 Storage 对这些行的修正（序号大于拉取前的链头位置）仍会被跟进重拉。重试也失败才丢掉没落盘的行。
	if job.staged != nil {
		ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		_, retryErr := job.staged.Commit(ctx, job.writes)
		cancel()
		if retryErr == nil {
			log.Infof("factor_flush_retry_ok source=%s subjects=%d", st.spec.Source, len(job.subjects))
			return
		}
		log.Errorf("factor_flush_retry_failed source=%s error=%v", st.spec.Source, retryErr)
	}
	if st.window != nil {
		for _, subject := range job.subjects {
			if err := st.window.Reload(context.Background(), d.cfg.Store, subject); err != nil {
				log.Errorf("factor_flush_recover_failed source=%s subject=%s error=%v", st.spec.Source, subject, err)
			}
		}
	}
}

// Drain 等所有数据源的后台落盘结束，引擎退出、关闭本地存储之前调用。
func (d *Data) Drain() {
	d.mu.Lock()
	var srcs []*source
	for _, st := range d.sources {
		srcs = append(srcs, st)
	}
	d.mu.Unlock()
	for _, st := range srcs {
		st.awaitFlush()
	}
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
	var failedSeries []seriesset.Series
	if plan.Multi() {
		// 统一数据集：实时周期（追赶读磁盘）按目标那一根逐序列判断；补算与只填充的区间只看这条序列有没有数据。
		// 整个标的都没有数据的（failed）不再重复列出。
		tagsOf := plan.TagsBySubject()
		live, target := plan.Mode == pipeline.ModeLive, plan.TargetEnd.Add(-step).Unix()
		for _, subject := range subjects {
			if got[subject] == nil {
				continue
			}
			for _, tag := range missingTagsAt(view, subject, tagsOf[subject], target, live) {
				failedSeries = append(failedSeries, seriesset.Series{SubjectID: subject, SeriesTag: tag})
			}
		}
	}
	return pipeline.DataResult{View: view, Frames: rows, Failed: failed, FailedSeries: failedSeries}, nil
}

// LongGap 是一个长时间没有数据的标的（设计 3.4，决定 19）。
type LongGap struct {
	Subject string
	// Rows 是缺口的根数，Duration 是对应的时长。
	Rows     int
	Duration time.Duration
	// Stale 为真表示标的到现在还没有新数据（Rows 是最新一根距今的根数）；否则是窗口里已经填充的缺口，填充值仍参与计算。
	Stale bool
}

// LongGapThreshold 是长缺口提示的阈值（设计 3.4，决定 19）：连续填充超过 24 小时；步长不小于 8 小时（如日线）时按 3 期，
// 否则每缺一天都会触发。
func LongGapThreshold(step time.Duration) int {
	if step >= 8*time.Hour {
		return 3
	}
	return int(24*time.Hour/step) + 1
}

// LongGaps 返回数据源里长时间没有数据的标的（按缺口从长到短）：窗口里连续填充超过阈值的缺口，以及到现在还没有新数据超过阈值的标的。
// 只提示，不阻断计算。缺失行只在之后有真实数据时才写，所以仍在持续的缺口表现为最新一根太旧，而不是结尾的填充行。
func (d *Data) LongGaps(src seriesstore.Source) []LongGap {
	st := d.lookup(src)
	if st == nil {
		return nil
	}
	window := st.win.Load()
	if window == nil {
		return nil
	}
	step, err := seriesstore.Step(src.Freq)
	if err != nil {
		return nil
	}
	threshold := LongGapThreshold(step)
	var out []LongGap
	seen := map[string]struct{}{}
	for subject, rows := range window.FilledRuns(threshold) {
		out = append(out, LongGap{Subject: subject, Rows: rows, Duration: time.Duration(rows) * step})
		seen[subject] = struct{}{}
	}
	now, err := d.axisNow(src)
	if err != nil {
		return out
	}
	for _, subject := range window.Subjects() {
		if _, dup := seen[subject]; dup {
			continue
		}
		if latest, ok := window.Latest(subject); ok {
			if behind := int(now.Sub(latest) / step); behind >= threshold {
				out = append(out, LongGap{Subject: subject, Rows: behind, Duration: time.Duration(behind) * step, Stale: true})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rows != out[j].Rows {
			return out[i].Rows > out[j].Rows
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

// WindowStatuses 实现 pipeline.Data：各任务内存窗口的载入状态。
func (d *Data) WindowStatuses() []pipeline.WarmupStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []pipeline.WarmupStatus
	for _, st := range d.sources {
		ids, regs := st.registered()
		var loaded map[string]struct{}
		if window := st.win.Load(); window != nil {
			loaded = make(map[string]struct{}, len(window.Subjects()))
			for _, subject := range window.Subjects() {
				loaded[subject] = struct{}{}
			}
		}
		for _, id := range ids {
			reg := regs[id]
			status := pipeline.WarmupStatus{TaskID: id, State: pipeline.WarmupWarming, ExpectedSubjects: len(reg.subjects)}
			for _, subject := range reg.subjects {
				if _, ok := loaded[subject]; ok {
					status.WarmSubjects++
				}
			}
			if st.ready.Load() {
				status.State = pipeline.WarmupReady
			}
			out = append(out, status)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
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
	d.lockSource(st, "变更跟进")
	defer st.mu.Unlock()
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

// Short 是回看根数不足的标的：本地连续数据的根数不到任务要求的回看，而 Storage 在本地最早一根之前还有数据（不是新上市的标的）。
type Short struct {
	Subject string
	// Have 是本地连续的根数，Want 是要求的回看根数。
	Have, Want int
	// Earliest 是本地最早的一根。
	Earliest time.Time
}

// Coverage 检查数据源每个标的的本地数据是否满足回看要求（设计 6.2）：根数 ≥ 最大回看根数，或者本地最早一根就是 Storage 的首根
// （在 Storage 保留期内上市、Storage 有多少本地就有多少的新标的）。不满足的列出来，提示导入。
// 没有本地数据的标的不在此列（同步失败由同步状态体现）。
func (d *Data) Coverage(ctx context.Context, src seriesstore.Source) ([]Short, error) {
	st := d.lookup(src)
	if st == nil {
		return nil, nil
	}
	st.mu.Lock()
	subjects, lookback, spec := append([]string(nil), st.subjects...), st.spec.Lookback, st.spec
	exempt := st.exemptNew
	st.mu.Unlock()
	step, err := seriesstore.Step(src.Freq)
	if err != nil {
		return nil, err
	}
	var short []Short
	for _, subject := range subjects {
		b, ok, err := d.cfg.Store.Bounds(src, subject)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		have := int(b.Latest.Sub(b.Earliest)/step) + 1
		if have >= lookback {
			continue
		}
		key := coverageKey{subject: subject, earliest: b.Earliest.Unix(), lookback: lookback}
		st.mu.Lock()
		_, known := st.covered[key]
		st.mu.Unlock()
		if known {
			continue
		}
		first, has, err := d.cfg.Storage.EarliestPeriod(ctx, src.SpaceID, src.DatasetID, src.Freq, []string{subject}, spec.Names())
		if err != nil {
			return nil, err
		}
		if exempt && (!has || !first.Before(b.Earliest)) {
			st.mu.Lock()
			if st.covered == nil {
				st.covered = map[coverageKey]struct{}{}
			}
			st.covered[key] = struct{}{}
			st.mu.Unlock()
			continue
		}
		short = append(short, Short{Subject: subject, Have: have, Want: lookback, Earliest: b.Earliest})
	}
	return short, nil
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
//   - 只在数据源已经准备好之后拉（数据源锁内，与变更跟进串行），新写入的行同时反映到窗口。没准备过的数据源不拉：
//     补算本来就读不到它，而且同步基线还没取，先写数据会漏掉之后的修正；Prepare 自己会拉到最新。
//   - 只拉数据源上登记的标的范围内、本地已有数据的标的（subjects 为空表示这个范围内的全部标的）。
//   - 统一数据集按各序列自己的进度拉，与实时路径相同：落后的序列不会被另一条序列的最新时间盖住而写成填充行。
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
	plan := pipeline.Plan{Set: set.Set, Expected: subjects, TargetStart: end, TargetEnd: end}
	if plan.ExpectedSeries, err = d.ExpectedSeries(ctx, plan); err != nil {
		return 0, err
	}
	staged, err := d.syncer.PullStagedTags(ctx, st.spec, subjects, plan.TagsBySubject(), end)
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

// syncWindow 把 CatchUp 的一次写入反映到窗口。数据集字段变了时窗口要按新字段重建，重建完成之前按预热处理（与实时路径相同）。
func (d *Data) syncWindow(ctx context.Context, st *source, subject string, res seriesstore.WriteResult) error {
	err := d.applyToWindow(ctx, st, subject, res)
	if errors.Is(err, serieswindow.ErrSchemaChanged) {
		st.ready.Store(false)
		return nil
	}
	return err
}
