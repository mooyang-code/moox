package view

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/follow"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	viewbleve "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/bleve"
	viewduckdb "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"

	"trpc.group/trpc-go/trpc-go/log"
)

// Service 是数据集查询索引（View）的服务：每个数据集恰好有一个 View，按 (空间, 数据集) 寻址。
type Service struct {
	// engines 键是引擎名，构造后只读。
	engines map[string]viewindex.Engine
	// mu 只保护 views 与下面的标量；索引的状态都在各自的 viewRuntime 上。
	mu             sync.RWMutex
	views          map[datasetRef]*viewRuntime
	primary        FieldReader
	metrics        *observability.ViewMetrics
	metadataClient MetadataClient
	readyPublisher ReadyEventPublisher
	// readyReconnect 替换就绪事件发布器的专用连接；发布超时或连接被关闭后异步调用，readyReconnecting 保证同一时刻只有一个重连。
	readyReconnect    func(context.Context) error
	readyReconnecting atomic.Bool

	// seriesBars 是时序 View 每个序列保留的根数，随查询响应告知调用方。
	seriesBars uint64
	// maxAge 是 View 按空间 × 频率的最长保留历史，超期的行不写入；maxAgeEnabled 为假时不限制。now 只供测试替换时钟。
	maxAge        storagepolicy.Retention
	maxAgeEnabled bool
	now           func() time.Time
	// follow 管理每个 View 跟随变更日志的协程；primaryRange 按时间范围回读时序行。
	follow       *follow.Manager
	primaryRange TimeSeriesRangeReader
	// maintenance 是维护参数（回填页大小、请求间隔、保留根数），断档重建沿用。
	maintenance MaintenanceOptions
	// buildMu 让首次建索引与断档重建一个接一个进行，不同时压在 Primary 上。
	buildMu sync.Mutex
}

// datasetRef 是一个 View 的身份：空间与数据集。
type datasetRef struct{ spaceID, datasetID string }

// viewSpec 是维护循环由数据集元数据组出的 View 声明，只在本包内部使用；每轮整体替换，发布后只读。
type viewSpec struct {
	SpaceID             string
	DatasetID           string
	DataKind            pb.DataKind
	Freq                string
	Status              string
	MaintenancePriority uint32
	// CreatedAt 是数据集的创建时刻（Unix 毫秒，元数据读出的值）：索引文件记下它，删除后按同一 ID 重建的数据集靠它认出旧索引不是自己的。
	CreatedAt int64
	// Columns 是数据集里启用的列，按建列顺序编号；HiddenColumns 是停用的列（只在返回结果时隐藏，数据保留）。
	Columns       []*viewindex.Column
	HiddenColumns []string
	// DisplayNames 是列的界面显示名，GetViewState 返回列时带上。
	DisplayNames map[string]string
}

func (v *viewSpec) ref() datasetRef { return datasetRef{spaceID: v.SpaceID, datasetID: v.DatasetID} }

// timeSeries 报告数据集是不是时序型：时序用 DuckDB、按根数保留、按时间范围回读；记录用 Bleve。
func (v *viewSpec) timeSeries() bool {
	return v != nil && v.DataKind == pb.DataKind_DATA_KIND_TIME_SERIES
}

// engineName 返回数据集的查询索引引擎：由数据类型决定，不存储。
func (v *viewSpec) engineName() string {
	if v.timeSeries() {
		return "duckdb"
	}
	return "bleve"
}

// newViewSpec 由数据集与它的全部列（按建列顺序）组出 View 声明。
func newViewSpec(dataset *pb.Dataset, columns []*pb.DatasetColumn) *viewSpec {
	spec := &viewSpec{
		SpaceID: dataset.GetSpaceId(), DatasetID: dataset.GetDatasetId(), DataKind: dataset.GetDataKind(),
		Freq: dataset.GetFreq(), Status: dataset.GetStatus(), MaintenancePriority: dataset.GetMaintenancePriority(),
		CreatedAt: dataset.GetCreatedAt(), DisplayNames: make(map[string]string, len(columns)),
	}
	for _, column := range columns {
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			continue
		}
		spec.DisplayNames[name] = column.GetDisplayName()
		if column.GetStatus() != "" && column.GetStatus() != "active" {
			spec.HiddenColumns = append(spec.HiddenColumns, name)
			continue
		}
		spec.Columns = append(spec.Columns, &viewindex.Column{ColumnName: name, ValueType: column.GetValueType(), SortOrder: uint32(len(spec.Columns))})
	}
	return spec
}

// viewPhase 描述一个 View 运行时的生命周期状态。每个数据集只有一个固定 indexID，
// 用显式状态机替代历史遗留的多代索引名字段。
type viewPhase uint8

const (
	// viewPhaseEmpty 表示尚未建好索引（新创建、首次建失败已丢弃、或已摘除）。
	viewPhaseEmpty viewPhase = iota
	// viewPhaseBuilding 表示维护协程正在执行首次建索引（失败时丢弃半成品并退回 viewPhaseEmpty）。
	viewPhaseBuilding
	// viewPhaseRebuilding 表示跟随器正在执行断档原地重建（或上次重建未完成，需由跟随器继续重建）。
	viewPhaseRebuilding
	// viewPhaseActive 表示活动索引可读且可跟随写入。
	viewPhaseActive
)

func (p viewPhase) isActive() bool   { return p == viewPhaseActive }
func (p viewPhase) isBuilding() bool { return p == viewPhaseBuilding || p == viewPhaseRebuilding }

// viewRuntime 是一个数据集 View 的全部内存状态。
//
// runtime 在 views 里只按 datasetRef 建一次：摘掉 View 前先停跟随器并拿 gate 关闭索引，之后不会再有写入方拿到这个
// runtime；同一数据集重建时是新的 runtime 和新的 gate。旧 runtime 的摘除删文件与新 runtime 的首次建索引都在维护协程里
// 串行执行，所以两把不同的 gate 不需要互相排斥。摘掉时整个 runtime 从 views 删掉，gate 与 revision 随之回收。
//
// 不被写索引卡住的东西不放在 mu 下：spec、phase、stats 进原子副本，fileOverLimit、revision、statsRefreshedAt 用原子量，
// ref、indexID、engine、gate 构造时定下、之后只读。查询路径只读 readState，不等 mu。
type viewRuntime struct {
	ref     datasetRef
	indexID string
	engine  viewindex.Engine
	gate    *indexWriteGate
	// revision 采用 Seqlock 偶稳奇写协议：偶数表示当前无引擎写入，奇数表示持有 gate 的引擎修改正在进行；
	// 引擎修改前后各加 1（进入置奇、退出置偶），仅受 mu 保护的索引切换路径加 2，分页读取方据此避免撕裂读。
	revision atomic.Uint64
	// fileOverLimit 是索引文件超过告警线：文件大小检查不能排在写索引后面。
	fileOverLimit    atomic.Bool
	statsRefreshedAt atomic.Int64

	// mu 只保护下面几项；写索引期间一直持有（大批因子写入要几秒）。
	mu    sync.Mutex
	phase viewPhase
	// schema 是当前索引的列契约（建索引中、重建中或已建好）。
	schema   viewindex.ViewIndexSchema
	spec     *viewSpec
	hasStats bool
	stats    viewindex.ViewIndexStats

	readState atomic.Pointer[runtimeReadState]
}

// runtimeReadState 是查询路径要的副本，随 phase、schema、spec 或统计一起整体替换。
type runtimeReadState struct {
	phase         viewPhase
	schema        viewindex.ViewIndexSchema
	spec          *viewSpec
	hasStats      bool
	stats         viewindex.ViewIndexStats
	hiddenColumns map[string]struct{}
}

func (s runtimeReadState) active() bool { return s.phase.isActive() }

// lastInitialRevision 保证同一进程内先后新建的 viewRuntime 也拿到严格递增的非零偶数初始修订号；
// 以当前 Unix 纳秒向上取偶为底则保证跨进程重启单调递增、永不为 0（Pin.Revision == 0 表示跳过校验），
// 且满足 Seqlock 的“偶稳奇写”不变式。
var lastInitialRevision atomic.Uint64

func nextInitialRevision() uint64 {
	now := uint64(max(time.Now().UnixNano(), 2))
	now = (now + 1) &^ 1
	for {
		prev := lastInitialRevision.Load()
		next := max(now, prev+2)
		if lastInitialRevision.CompareAndSwap(prev, next) {
			return next
		}
	}
}

// newViewRuntime 创建运行时并立即发布一份查询路径副本：没有发布过副本的运行时在读取状态时要等运行时锁。
// revision 以非零偶数且跨进程重启单调递增的基数初始化，避免重启后首次写入前多页翻页跳过版本校验。
func newViewRuntime(spec *viewSpec, engine viewindex.Engine) *viewRuntime {
	ref := spec.ref()
	runtime := &viewRuntime{ref: ref, indexID: viewindex.IndexID(ref.spaceID, ref.datasetID), engine: engine, gate: newIndexWriteGate(), spec: spec}
	runtime.revision.Store(nextInitialRevision())
	runtime.publishReadStateLocked()
	return runtime
}

// beginEngineWrite 标记一次受 gate 保护的引擎修改开始（将偶数 revision 推为奇数），
// 调用方必须在释放 gate 之前执行返回的收尾函数（即使修改失败也要恢复偶数，防止永久卡在奇数状态）。
func (r *viewRuntime) beginEngineWrite() func() {
	r.revision.Add(1)
	return func() {
		r.revision.Add(1)
	}
}

// bumpRevision 推进仅受 mu 保护的索引切换修订号（步长 2），与 gate 下的奇偶不变式完全正交。
func (r *viewRuntime) bumpRevision() {
	r.revision.Add(2)
}

// needsRebuild 报告 View 是否处于断档原地重建状态（断档、重建中途失败或重启恢复前未完成）：
// 此时维护流程不另起首次建索引，也不在建成前挂回活动索引，由跟随器接着重建。
func (r *viewRuntime) needsRebuild() bool {
	return r.queryState().phase == viewPhaseRebuilding
}

// publishReadStateLocked 重新发布查询路径副本；改了 phase、schema、spec 或统计后在持有 mu 时调用。
func (r *viewRuntime) publishReadStateLocked() {
	hidden := make(map[string]struct{}, len(r.spec.HiddenColumns))
	for _, name := range r.spec.HiddenColumns {
		hidden[name] = struct{}{}
	}
	r.readState.Store(&runtimeReadState{
		phase: r.phase, schema: r.schema, spec: r.spec,
		hasStats: r.hasStats, stats: r.stats, hiddenColumns: hidden,
	})
}

// clearActiveLocked 清空活动状态及统计缓存（调用方持有 mu）；若原先处于活动态则递增修订号，让在途查询感知切换。
func (r *viewRuntime) clearActiveLocked() {
	if r.phase.isActive() {
		r.bumpRevision()
	}
	r.phase = viewPhaseEmpty
	r.hasStats = false
	r.stats = viewindex.ViewIndexStats{}
	r.publishReadStateLocked()
}

// queryState 返回查询路径副本，不等 mu。
func (r *viewRuntime) queryState() runtimeReadState {
	return *r.readState.Load()
}

func New(root string) (*Service, error) {
	bleveEngine, err := viewbleve.Open(viewbleve.Options{Path: filepath.Join(root, "bleve")})
	if err != nil {
		return nil, err
	}
	engines := map[string]viewindex.Engine{"bleve": bleveEngine}
	duckdbEngine, err := viewduckdb.OpenIndexManager(viewduckdb.IndexManagerOptions{Root: filepath.Join(root, "duckdb")})
	if err == nil {
		engines["duckdb"] = duckdbEngine
	} else if !viewduckdb.IsUnavailable(err) {
		return nil, fmt.Errorf("open duckdb view indexes: %w", err)
	}
	return &Service{engines: engines, views: make(map[datasetRef]*viewRuntime), metrics: observability.DefaultViewMetrics}, nil
}

// SetMetrics replaces the aggregate view metrics sink. It is intended for
// tests or a process that supplies a dedicated registerer, before consumption
// starts.
func (s *Service) SetMetrics(metrics *observability.ViewMetrics) {
	if s == nil {
		return
	}
	if metrics == nil {
		metrics = observability.DefaultViewMetrics
	}
	s.metrics = metrics
}

// runtimeOf 返回数据集的运行时，没有时返回 nil。
func (s *Service) runtimeOf(ref datasetRef) *viewRuntime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.views[ref]
}

// ensureRuntime 返回数据集的运行时，没有时按声明新建。已有的运行时引擎与声明不一致（数据集删除后按同一 ID 以另一种
// 数据类型重建）时，先摘掉旧的并删除它的索引文件。
func (s *Service) ensureRuntime(ctx context.Context, spec *viewSpec) (*viewRuntime, error) {
	engine := s.engines[spec.engineName()]
	if engine == nil {
		return nil, fmt.Errorf("view engine %q is unavailable", spec.engineName())
	}
	ref := spec.ref()
	if runtime := s.runtimeOf(ref); runtime != nil {
		if runtime.engine == engine {
			return runtime, nil
		}
		s.detachView(ctx, runtime, true)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if runtime := s.views[ref]; runtime != nil {
		return runtime, nil
	}
	runtime := newViewRuntime(spec, engine)
	s.views[ref] = runtime
	return runtime, nil
}

// detachView 摘掉一个 View：停跟随器，清掉活动状态并从 views 删掉；removeFiles 为真时删除索引文件（数据集已删除），
// 否则保留（数据集停用，重新启用后挂回）。
func (s *Service) detachView(ctx context.Context, runtime *viewRuntime, removeFiles bool) {
	s.followManager().Stop(runtime.ref.spaceID, runtime.ref.datasetID)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.clearActiveLocked()
	s.mu.Lock()
	// 只有确实摘掉了这个运行时才删文件：映射里已经换成别的运行时时，文件属于新的那个。
	owned := s.views[runtime.ref] == runtime
	if owned {
		delete(s.views, runtime.ref)
	}
	s.mu.Unlock()
	if owned && removeFiles {
		// 跟随器已停、View 已不在 views 里，新的查询找不到它；拿索引写锁等正在进行的写入、加列、删除最旧数据结束后
		// 关闭并删除文件（引擎关闭时等已经开始的查询跑完）。删不掉只记日志，文件留到下次启动时按孤立文件清理。
		s.removeDetachedIndex(ctx, runtime)
	}
}

// removeDetachedIndex 删除刚摘除的 View 的索引文件。调用方持有运行时锁，View 已不在 views 里。
func (s *Service) removeDetachedIndex(ctx context.Context, runtime *viewRuntime) {
	// 停服时也要删完：这一步不跟着请求取消。
	ctx = context.WithoutCancel(ctx)
	release, err := runtime.gate.lock(ctx)
	if err == nil {
		err = runtime.engine.Remove(ctx, runtime.indexID)
		release()
	}
	if err != nil {
		log.Errorf("storage view 删除已摘除 View 的索引文件失败，留到下次启动清理 index_id=%s：%v", runtime.indexID, err)
	}
}

func (s *Service) writeIndex(ctx context.Context, runtime *viewRuntime, batch viewindex.ViewIndexWriteBatch) error {
	gateStart := time.Now()
	release, err := runtime.gate.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	if wait := time.Since(gateStart); wait >= 100*time.Millisecond {
		log.Warnf("storage view 索引写锁等待较久 index=%s 等待=%s", runtime.indexID, wait.Round(time.Millisecond))
	}
	// 所有写入路径（实时跟随、首次回填）都经过这里，统一丢弃超过最长保留期的行。
	if batch.RowWrites = s.dropExpiredWrites(runtime.indexID, batch.RowWrites); len(batch.RowWrites) == 0 {
		return nil
	}
	endWrite := runtime.beginEngineWrite()
	defer endWrite()
	return runtime.engine.Write(ctx, runtime.indexID, batch)
}

func (s *Service) HasEngine(name string) bool {
	if s == nil {
		return false
	}
	return s.engines[strings.ToLower(strings.TrimSpace(name))] != nil
}

var _ pb.DataViewService = (*Service)(nil)

// prepareIndex 把 View 的索引清成一个空索引，准备回填。schemaOf 在运行时锁内给出要建的列契约，给不出时什么都不动。
// 持有运行时锁准备：实时写入、加列、删除最旧数据都在运行时锁下看 phase，不会碰到准备了一半的文件。
// 从这里开始 View 不可读（phase 切到 targetPhase），直到建成后转为 viewPhaseActive；中途失败时由调用方决定丢弃还是重试。
func (s *Service) prepareIndex(ctx context.Context, runtime *viewRuntime, targetPhase viewPhase, schemaOf func(*viewRuntime) (viewindex.ViewIndexSchema, error)) (viewindex.ViewIndexSchema, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	schema, err := schemaOf(runtime)
	if err != nil {
		return viewindex.ViewIndexSchema{}, err
	}
	if runtime.phase.isActive() {
		runtime.bumpRevision()
	}
	runtime.phase = targetPhase
	runtime.hasStats = false
	runtime.stats = viewindex.ViewIndexStats{}
	runtime.schema = schema
	runtime.publishReadStateLocked()
	release, err := runtime.gate.lock(ctx)
	if err != nil {
		return viewindex.ViewIndexSchema{}, err
	}
	defer release()
	// 原来的索引或上次没建完的半成品（含 DuckDB 的 WAL）先删干净。
	if err := runtime.engine.Remove(ctx, runtime.indexID); err != nil {
		return viewindex.ViewIndexSchema{}, err
	}
	if err := runtime.engine.Prepare(ctx, runtime.indexID, schema); err != nil {
		return viewindex.ViewIndexSchema{}, err
	}
	return schema, nil
}

func (s *Service) SetPrimaryReader(reader FieldReader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.primary = reader
}

func (s *Service) setMetadataClient(metadata MetadataClient) {
	s.mu.Lock()
	s.metadataClient = metadata
	s.mu.Unlock()
}

func (s *Service) metadataClientSnapshot() MetadataClient {
	s.mu.RLock()
	metadata := s.metadataClient
	s.mu.RUnlock()
	return metadata
}

var errViewIndexNotReady = errors.New("view index not ready")

// activeIndex 返回数据集可读的活动索引与运行时：只取一次全局读锁，之后读原子副本。
func (s *Service) activeIndex(spaceID, datasetID string) (string, *viewRuntime) {
	runtime := s.runtimeOf(datasetRef{spaceID: spaceID, datasetID: datasetID})
	if runtime == nil {
		return "", nil
	}
	if !runtime.queryState().active() {
		return "", runtime
	}
	return runtime.indexID, runtime
}

// engineFor 返回可读索引的引擎；没有运行时或没有活动索引时返回“还没就绪”。
func engineFor(runtime *viewRuntime, indexID string) (viewindex.Engine, error) {
	if runtime == nil || indexID == "" {
		return nil, fmt.Errorf("view index is not prepared: %w", errViewIndexNotReady)
	}
	return runtime.engine, nil
}

func (s *Service) cacheActiveIndexStats(runtime *viewRuntime, indexID string, stats viewindex.ViewIndexStats) {
	if runtime == nil || indexID == "" {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.phase.isActive() || runtime.indexID != indexID {
		return
	}
	runtime.hasStats = true
	runtime.stats = stats
	runtime.publishReadStateLocked()
}

func cachedActiveIndexStats(runtime *viewRuntime, indexID string) (viewindex.ViewIndexStats, bool) {
	if runtime == nil || indexID == "" || runtime.indexID != indexID {
		return viewindex.ViewIndexStats{}, false
	}
	state := runtime.queryState()
	if !state.active() || !state.hasStats {
		return viewindex.ViewIndexStats{}, false
	}
	return state.stats, true
}

// tryCacheActiveIndexStats 是查询路径用的缓存统计：运行时锁正被写索引占着时直接放弃，查询不等写入。
func tryCacheActiveIndexStats(runtime *viewRuntime, indexID string, stats viewindex.ViewIndexStats) {
	if runtime == nil || indexID == "" || !runtime.mu.TryLock() {
		return
	}
	defer runtime.mu.Unlock()
	if !runtime.phase.isActive() || runtime.indexID != indexID {
		return
	}
	runtime.hasStats = true
	runtime.stats = stats
	runtime.publishReadStateLocked()
}
