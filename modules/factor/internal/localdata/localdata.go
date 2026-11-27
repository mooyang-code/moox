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
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/seriessync"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
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

// ErrNoLocalData 表示数据源在本地时序存储里一个标的都没有：补算只读本地，没有可补的数据。
var ErrNoLocalData = errors.New("本地没有这个数据集的数据，没有可补的数据")

// NoDataError 表示任务里的标的在本地和 Storage 全都没有任何数据（设计 6.2，决定 24）：数据源未就绪，
// 要导入历史数据，或把这些标的从源数据集的标签里移除。只有部分标的没有数据时（例如停牌超过保留期的股票）
// 不算未就绪：记一条警告，这些标的每期按本期不可用处理，其余标的照常计算（任务检查里另有一项列出它们）。
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

	// hotWrites 是走热路径（窗口先追加、写盘并行）的标的写入累计数，coldWrites 是同步写盘的累计数。
	hotWrites, coldWrites atomic.Int64
}

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
	// dropped 在 dropSourceLocked 移除该数据源时置真（受 mu 保护）；
	// 并发拿到旧 *source 指针后再加 mu 的调用方据此识别已丢弃实例并重试或跳过，避免操作孤立 *source。
	dropped bool
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

// SourceInfo 是一个已准备的数据源及其任务。
type SourceInfo struct {
	Source seriesstore.Source
	TaskID string
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
	if st.dropped {
		return
	}
	d.settleFlush(st)
	st.regsMu.Lock()
	delete(st.regs, set.TaskID)
	empty := len(st.regs) == 0
	st.regsMu.Unlock()
	if !empty {
		return
	}
	d.dropSourceLocked(src, st)
}

// RetainTasks 仅保留 active 中活跃任务的登记，清理已离开目录或预载中途失败残留的任务登记；
// 若某数据源已不再被任何活跃任务引用，同步释放其窗口、内存预算与拉取状态，不误删仍被其他任务共享的数据源。
// 返回本次被彻底释放的数据源列表（按名称排序）。
func (d *Data) RetainTasks(active map[string]struct{}) []seriesstore.Source {
	type entry struct {
		src seriesstore.Source
		st  *source
	}
	d.mu.Lock()
	snapshot := make([]entry, 0, len(d.sources))
	for src, st := range d.sources {
		snapshot = append(snapshot, entry{src: src, st: st})
	}
	d.mu.Unlock()

	var dropped []seriesstore.Source
	for _, item := range snapshot {
		if d.retainSourceTasks(item.src, item.st, active) {
			dropped = append(dropped, item.src)
		}
	}
	sort.Slice(dropped, func(i, j int) bool { return dropped[i].String() < dropped[j].String() })
	return dropped
}

func (d *Data) retainSourceTasks(src seriesstore.Source, st *source, active map[string]struct{}) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.dropped {
		return false
	}
	st.regsMu.Lock()
	var stale []string
	for id := range st.regs {
		if _, ok := active[id]; !ok {
			stale = append(stale, id)
		}
	}
	st.regsMu.Unlock()
	if len(stale) == 0 {
		return false
	}
	d.settleFlush(st)
	st.regsMu.Lock()
	for _, id := range stale {
		delete(st.regs, id)
	}
	empty := len(st.regs) == 0
	st.regsMu.Unlock()
	if !empty {
		return false
	}
	d.dropSourceLocked(src, st)
	return true
}

// dropSourceLocked 在已持有 st.mu 且确认没有任何任务登记在 st 上时，彻底移除该数据源的运行期关联状态。
// 先置 dropped 并清理拉取状态与内存配额，最后再从 d.sources 移除，保证并发 Prepare 在拿到 st.mu 并重试之前
// 不会创建同 src 的新 *source 实例与这里的清理产生交错。
func (d *Data) dropSourceLocked(src seriesstore.Source, st *source) {
	if st.dropped {
		return
	}
	st.dropped = true
	st.ready.Store(false)
	st.win.Store(nil)
	if d.syncer != nil {
		d.syncer.ClearPulled(src)
	}
	if d.cfg.Admit != nil {
		d.cfg.Admit.SetResident("window:"+src.String(), 0)
	}
	d.mu.Lock()
	if d.sources[src] == st {
		delete(d.sources, src)
	}
	d.mu.Unlock()
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

// axisNow 是当前时刻在数据源轴上的排他终点：最新已收盘一根的下一期。
func (d *Data) axisNow(src seriesstore.Source) (time.Time, error) {
	axis, err := periodclock.AxisFor(src.SpaceID, src.Freq)
	if err != nil {
		return time.Time{}, err
	}
	return axis.AxisEnd(d.cfg.Now())
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

// st 返回数据源状态（测试用）。
func (d *Data) st(src seriesstore.Source) *source { return d.lookup(src) }

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

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
