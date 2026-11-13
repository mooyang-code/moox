package view

import (
	"context"
	"crypto/hmac"
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
	"github.com/mooyang-code/moox/modules/storage/internal/serviceauth"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"google.golang.org/protobuf/proto"

	"trpc.group/trpc-go/trpc-go/log"
)

type Service struct {
	engines      map[string]viewindex.Engine
	indexEngine  map[string]string
	schemas      map[string]viewindex.ViewIndexSchema
	views        map[viewRef]*viewRuntime
	catalogViews map[viewRef]*pb.View
	indexView    map[string]viewRef
	authSecret   string
	// writerAuthSecret 是 Primary 角色密钥：持写入身份的调用方（Collector、moox-cli 导入）也能查 View 状态、等 View 跟上自己的写入。
	writerAuthSecret string
	primaryAuth      *pb.AuthInfo
	primary          FieldReader
	mu               sync.RWMutex
	metrics          *observability.ViewMetrics
	metadataClient   MetadataClient
	readyPublisher   ReadyEventPublisher
	indexGatesMu     sync.Mutex
	indexGates       map[string]*indexWriteGate
	// indexRevision advances after every successful write to an index, so a
	// reader that is paginating detects in-place corrections and deletions.
	indexRevision     map[string]uint64
	cleanupMu         sync.Mutex
	cleanupCandidates map[managedIndexRef]retiredIndexCandidate
	fileOverLimitMu   sync.Mutex
	fileOverLimit     map[viewRef]struct{}
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

type viewRef struct{ spaceID, viewID string }
type viewRuntime struct {
	mu     sync.Mutex
	active string
	// building 是正在建的索引（首次建索引或断档重建），建成后转为 active，期间 active 为空。任何时刻只写一个索引。
	building         string
	statsIndexID     string
	stats            viewindex.ViewIndexStats
	statsRefreshedAt time.Time
	// hiddenColumns 是逻辑上已删除的列（元数据随 View 下发）：只在返回结果时过滤，索引和已写入的值都不动。
	hiddenColumns map[string]struct{}
	// readState is a lock-free copy of what queries need. An index write holds mu
	// for its whole index write (seconds for a large factor batch), so queries
	// that took mu to read the active index waited behind every write.
	readState atomic.Pointer[runtimeReadState]
}

// runtimeReadState is the query-path view of a runtime: the readable index
// and the stats cached for it.
type runtimeReadState struct {
	active       string
	statsIndexID string
	stats        viewindex.ViewIndexStats
	// hiddenColumns 发布后只读，更新时整体替换。
	hiddenColumns map[string]struct{}
}

// newViewRuntime 创建运行时并立即发布一份空的查询路径副本：没有发布过副本的运行时在读取 active 时要等运行时锁，
// 首次构建期间整段索引写入都持有它，会拖住持有全局串行锁的就绪刷新。
func newViewRuntime() *viewRuntime {
	runtime := &viewRuntime{}
	runtime.publishReadStateLocked()
	return runtime
}

// needsRebuild 报告 View 是否要原地重建或正在重建（断档、重建中途失败或退出、活动索引文件不见了）：此时元数据仍说
// View 是活动的，维护流程不能在建成前把它挂回去，跟随器据此接着重建。
//
// 首次建索引期间同样是“没有活动索引、有建索引目标”，但它只在维护协程里、持有 buildMu 一次做完：开头先停掉跟随器，
// 失败时 discardInitialBuild 清掉 building，建成后转为 active，这段时间里没有别的调用方读这个判断。所以长期保持
// 这个状态的只有未完成的重建。
func (r *viewRuntime) needsRebuild() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active == "" && r.building != ""
}

// publishReadStateLocked republishes the query-path copy; call it with mu held
// after changing active, statsIndexID or stats.
func (r *viewRuntime) publishReadStateLocked() {
	r.readState.Store(&runtimeReadState{active: r.active, statsIndexID: r.statsIndexID, stats: r.stats, hiddenColumns: r.hiddenColumns})
}

// clearActiveLocked 清空活动索引及其统计缓存（调用方持有 mu）。
func (r *viewRuntime) clearActiveLocked() {
	r.active = ""
	r.statsIndexID = ""
	r.stats = viewindex.ViewIndexStats{}
	r.publishReadStateLocked()
}

// queryState returns the query-path copy without waiting for mu. A runtime
// that never published one (built directly in tests) falls back to mu.
func (r *viewRuntime) queryState() runtimeReadState {
	if state := r.readState.Load(); state != nil {
		return *state
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return runtimeReadState{active: r.active, statsIndexID: r.statsIndexID, stats: r.stats, hiddenColumns: r.hiddenColumns}
}

func New(root, authSecret string) (*Service, error) {
	if strings.TrimSpace(authSecret) == "" {
		return nil, errors.New("view auth secret is required")
	}
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
	service := &Service{
		engines:           engines,
		indexEngine:       make(map[string]string),
		schemas:           make(map[string]viewindex.ViewIndexSchema),
		views:             make(map[viewRef]*viewRuntime),
		catalogViews:      make(map[viewRef]*pb.View),
		indexView:         make(map[string]viewRef),
		authSecret:        authSecret,
		indexGates:        make(map[string]*indexWriteGate),
		indexRevision:     make(map[string]uint64),
		cleanupCandidates: make(map[managedIndexRef]retiredIndexCandidate),
		metrics:           observability.DefaultViewMetrics,
	}
	return service, nil
}

func (s *Service) nextIndexRevision(indexID string) uint64 {
	if s == nil || indexID == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexRevision == nil {
		s.indexRevision = make(map[string]uint64)
	}
	s.indexRevision[indexID]++
	return s.indexRevision[indexID]
}

func (s *Service) indexRevisionOf(indexID string) uint64 {
	if s == nil || indexID == "" {
		return 0
	}
	s.mu.RLock()
	revision := s.indexRevision[indexID]
	s.mu.RUnlock()
	return revision
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

func (s *Service) indexWriteGate(indexID string) *indexWriteGate {
	s.indexGatesMu.Lock()
	defer s.indexGatesMu.Unlock()
	if s.indexGates == nil {
		s.indexGates = make(map[string]*indexWriteGate)
	}
	gate := s.indexGates[indexID]
	if gate == nil {
		gate = newIndexWriteGate()
		s.indexGates[indexID] = gate
	}
	return gate
}

func (s *Service) writeIndex(ctx context.Context, indexID string, engine viewindex.Engine, batch viewindex.ViewIndexWriteBatch) error {
	if engine == nil {
		return errors.New("view index engine is unavailable")
	}
	gateStart := time.Now()
	release, err := s.indexWriteGate(indexID).lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	if wait := time.Since(gateStart); wait >= 100*time.Millisecond {
		log.Warnf("storage view 索引写锁等待较久 index=%s 等待=%s", indexID, wait.Round(time.Millisecond))
	}
	// 所有写入路径（实时事件、补采、首次回填）都经过这里，统一丢弃超过最长保留期的行。
	if batch.RowWrites = s.dropExpiredWrites(indexID, batch.RowWrites); len(batch.RowWrites) == 0 {
		return nil
	}
	if err := engine.Write(ctx, indexID, batch); err != nil {
		return err
	}
	s.nextIndexRevision(indexID)
	return nil
}

func (s *Service) HasEngine(name string) bool {
	if s == nil {
		return false
	}
	return s.engines[strings.ToLower(strings.TrimSpace(name))] != nil
}

var _ pb.DataViewService = (*Service)(nil)

// prepareIndex 删掉同名的半成品后按 schema 建一个空索引，并把它记为 View 的建索引目标。建索引只在进程内进行，View 不对外提供索引写入接口。
func (s *Service) prepareIndex(ctx context.Context, indexID string, schema viewindex.ViewIndexSchema) error {
	engine := s.engines[schema.Engine]
	if engine == nil {
		return fmt.Errorf("view engine %q is unavailable", schema.Engine)
	}
	viewKey := viewRef{spaceID: schema.SpaceID, viewID: schema.ViewID}
	s.mu.Lock()
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = newViewRuntime()
		s.views[viewKey] = runtime
	}
	s.mu.Unlock()
	// 持有运行时锁准备：实时写入、删除和清理都在运行时锁下看 active 与 building，不会碰到准备了一半的文件。
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active == indexID {
		return fmt.Errorf("View 索引 %q 是活动索引，不能重新准备", indexID)
	}
	release, err := s.indexWriteGate(indexID).lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	// 上次没建完的半成品（含 DuckDB 的 WAL）先删干净。
	if err := engine.Remove(ctx, indexID); err != nil {
		return err
	}
	if err := engine.Prepare(ctx, indexID, schema); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexEngine[indexID] = schema.Engine
	s.schemas[indexID] = schema
	if s.catalogViews[viewKey] == nil {
		s.catalogViews[viewKey] = &pb.View{SpaceId: schema.SpaceID, ViewId: schema.ViewID, DatasetId: schema.PrimaryDatasetID, Engine: schema.Engine}
	}
	runtime.building = indexID
	s.indexView[indexID] = viewKey
	return nil
}

// runtimeOfIndex 返回索引所属 View 的运行时：先按索引 ID 解析所属 View（与清理路径一致），解析不了的退回索引到
// View 的映射；View 还没有运行时返回空。
func (s *Service) runtimeOfIndex(indexID string) *viewRuntime {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if parsed, err := viewindex.ParseViewIndexID(indexID); err == nil {
		return s.views[viewRef{spaceID: parsed.SpaceID, viewID: parsed.ViewID}]
	}
	if key, ok := s.indexView[indexID]; ok {
		return s.views[key]
	}
	return nil
}

func (s *Service) SetPrimaryAuth(auth *pb.AuthInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if auth == nil {
		s.primaryAuth = nil
		return
	}
	s.primaryAuth = proto.Clone(auth).(*pb.AuthInfo)
}

func (s *Service) primaryAuthSnapshot() *pb.AuthInfo {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.primaryAuth == nil {
		return nil
	}
	return proto.Clone(s.primaryAuth).(*pb.AuthInfo)
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

func (s *Service) engineFor(id string) (viewindex.Engine, error) {
	s.mu.RLock()
	name := s.indexEngine[id]
	engine := s.engines[name]
	s.mu.RUnlock()
	if engine == nil {
		return nil, fmt.Errorf("view index %q is not prepared: %w", id, errViewIndexNotReady)
	}
	return engine, nil
}

var errViewIndexNotReady = errors.New("view index not ready")

func (s *Service) activeIndex(spaceID, viewID string) (string, *viewRuntime) {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime != nil {
		return runtime.queryState().active, runtime
	}
	return viewID, nil
}

func (s *Service) cacheActiveIndexStats(runtime *viewRuntime, indexID string, stats viewindex.ViewIndexStats) {
	if runtime == nil || indexID == "" {
		return
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active != indexID {
		return
	}
	runtime.statsIndexID = indexID
	runtime.stats = stats
	runtime.publishReadStateLocked()
}

func cachedActiveIndexStats(runtime *viewRuntime, indexID string) (viewindex.ViewIndexStats, bool) {
	if runtime == nil || indexID == "" {
		return viewindex.ViewIndexStats{}, false
	}
	state := runtime.queryState()
	if state.active != indexID || state.statsIndexID != indexID {
		return viewindex.ViewIndexStats{}, false
	}
	return state.stats, true
}

// forgetIndexLocked 去掉索引的全部内存映射（调用方持有 s.mu）。
func (s *Service) forgetIndexLocked(id string) {
	delete(s.indexEngine, id)
	delete(s.schemas, id)
	delete(s.indexView, id)
}

func (s *Service) authorize(auth *pb.AuthInfo) error {
	if s == nil || strings.TrimSpace(s.authSecret) == "" {
		return errors.New("view auth is not configured")
	}
	if auth == nil || strings.TrimSpace(auth.GetAppId()) == "" || strings.TrimSpace(auth.GetAppKey()) == "" {
		return errors.New("view auth is required")
	}
	expected := serviceauth.Key(s.authSecret, auth.GetAppId())
	if !hmac.Equal([]byte(strings.ToLower(auth.GetAppKey())), []byte(expected)) {
		return errors.New("invalid view HMAC")
	}
	return nil
}

// SetWriterAuthSecret 设置 Primary 角色密钥，GetViewState 与 WaitViewApplied 接受由它派生的写入身份。
func (s *Service) SetWriterAuthSecret(secret string) {
	s.mu.Lock()
	s.writerAuthSecret = strings.TrimSpace(secret)
	s.mu.Unlock()
}

// authorizeViewOrWriter 接受 View 角色身份，或由 Primary 角色密钥派生的写入身份：写入方只用它确认自己的写入已经可查。
func (s *Service) authorizeViewOrWriter(auth *pb.AuthInfo) error {
	viewErr := s.authorize(auth)
	if viewErr == nil {
		return nil
	}
	s.mu.RLock()
	secret := s.writerAuthSecret
	s.mu.RUnlock()
	if secret == "" || auth == nil || strings.TrimSpace(auth.GetAppId()) == "" {
		return viewErr
	}
	if hmac.Equal([]byte(strings.ToLower(auth.GetAppKey())), []byte(serviceauth.Key(secret, auth.GetAppId()))) {
		return nil
	}
	return viewErr
}
