package view

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	viewbleve "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/bleve"
	viewduckdb "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"google.golang.org/protobuf/proto"
)

type Service struct {
	engines                    map[string]viewindex.Engine
	indexEngine                map[string]string
	schemas                    map[string]viewindex.ViewIndexSchema
	views                      map[viewRef]*viewRuntime
	catalogViews               map[viewRef]*pb.View
	indexView                  map[string]viewRef
	authSecret                 string
	primaryAuth                *pb.AuthInfo
	primary                    FieldReader
	mu                         sync.RWMutex
	byData                     map[datasetRef]map[string]struct{}
	metrics                    *observability.ViewMetrics
	periodMetadata             PeriodMetadataClient
	metadataClient             MetadataClient
	readyPublisher             ReadyEventPublisher
	consumerBounds             map[string]func() bool
	consumerPartitionByDataset map[datasetRef]string
	indexGatesMu               sync.Mutex
	indexGates                 map[string]*indexWriteGate
	// indexRevision advances after every successful write to an index, so a
	// reader that is paginating detects in-place corrections and deletions.
	indexRevision     map[string]uint64
	cleanupMu         sync.Mutex
	cleanupCandidates map[managedIndexRef]retiredIndexCandidate
	fileOverLimitMu   sync.Mutex
	fileOverLimit     map[viewRef]struct{}
	pendingReadyMu    sync.Mutex
	pendingReady      []pendingViewReady
	readyFlushMu      sync.Mutex
	readyFenceDir     string
	appliedFenceMu    sync.Mutex
	appliedFence      map[appliedFenceKey]uint64

	// readyReconnect 替换就绪事件发布器的专用连接；发布超时或连接被关闭后在刷新锁之外异步调用。
	readyReconnect func(context.Context) error
	// readyBackoffUntil 之前不再尝试发布就绪事件（上一次发布因连接问题失败）；readyReconnecting 保证同一时刻只有一个重连。
	readyBackoffUntil time.Time
	readyReconnecting atomic.Bool
	// readyIssues 是就绪队列每类异常最近的一条日志，用于去重；appliedPersistMu 串行化写入围栏的落盘；
	// appliedDirty 表示写入围栏有尚未落盘的更新（受 appliedFenceMu 保护）。
	readyLogMu       sync.Mutex
	readyIssues      map[string]string
	appliedPersistMu sync.Mutex
	appliedDirty     bool
	// seriesBars 是时序 View 每个序列保留的根数，随查询响应告知调用方。
	seriesBars uint64
	// maxAge 是 View 按空间 × 频率的最长保留历史，超期的行不写入；maxAgeEnabled 为假时不限制。now 只供测试替换时钟。
	maxAge        storagepolicy.Retention
	maxAgeEnabled bool
	now           func() time.Time
}

type datasetRef struct{ spaceID, datasetID string }
type viewRef struct{ spaceID, viewID string }
type viewRuntime struct {
	mu     sync.Mutex
	active string
	// building 是首次建索引期间的写入目标，建成后转为 active。任何时刻只写一个索引。
	building         string
	statsIndexID     string
	stats            viewindex.ViewIndexStats
	statsRefreshedAt time.Time
	// hiddenColumns 是逻辑上已删除的列（元数据随 View 下发）：只在返回结果时过滤，索引和已写入的值都不动。
	hiddenColumns map[string]struct{}
	// readState is a lock-free copy of what queries need. A delivery holds mu
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

// writeTargetLocked 返回实时写入的目标索引：活动索引，或首次建索引期间的 building（调用方持有 mu）。
func (r *viewRuntime) writeTargetLocked() string {
	if r.active != "" {
		return r.active
	}
	return r.building
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

func (s *Service) attachOwnedDatasetMappingLocked(spaceID, datasetID, indexID string) {
	spaceID = strings.TrimSpace(spaceID)
	datasetID = strings.TrimSpace(datasetID)
	indexID = strings.TrimSpace(indexID)
	if spaceID == "" || datasetID == "" || indexID == "" {
		return
	}
	ref := datasetRef{spaceID: spaceID, datasetID: datasetID}
	if s.byData[ref] == nil {
		s.byData[ref] = make(map[string]struct{})
	}
	s.byData[ref][indexID] = struct{}{}
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
		engines:                    engines,
		indexEngine:                make(map[string]string),
		schemas:                    make(map[string]viewindex.ViewIndexSchema),
		views:                      make(map[viewRef]*viewRuntime),
		catalogViews:               make(map[viewRef]*pb.View),
		indexView:                  make(map[string]viewRef),
		authSecret:                 authSecret,
		byData:                     make(map[datasetRef]map[string]struct{}),
		indexGates:                 make(map[string]*indexWriteGate),
		indexRevision:              make(map[string]uint64),
		cleanupCandidates:          make(map[managedIndexRef]retiredIndexCandidate),
		consumerBounds:             make(map[string]func() bool),
		consumerPartitionByDataset: make(map[datasetRef]string),
		metrics:                    observability.DefaultViewMetrics,
		readyFenceDir:              filepath.Join(root, "view-data-ready"),
		appliedFence:               make(map[appliedFenceKey]uint64),
	}
	if err := service.loadReadyFence(); err != nil {
		return nil, err
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
		log.Printf("storage view 索引写锁等待较久 index=%s 等待=%s", indexID, wait.Round(time.Millisecond))
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

var _ pb.ViewIndexService = (*Service)(nil)
var _ pb.DataViewService = (*Service)(nil)

func (s *Service) PrepareViewIndex(ctx context.Context, req *pb.PrepareViewIndexReq) (*pb.PrepareViewIndexRsp, error) {
	if req == nil || req.GetSchema() == nil || req.GetIndexId() == "" {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id and schema are required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	sch := req.GetSchema()
	engineName := strings.ToLower(strings.TrimSpace(sch.GetEngine()))
	if engineName == "" {
		engineName = strings.ToLower(strings.TrimSpace(req.GetEngine()))
	}
	engine := s.engines[engineName]
	if engine == nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, fmt.Errorf("view engine %q is unavailable", engineName))}, nil
	}
	schema := viewindex.ViewIndexSchema{SpaceID: sch.GetSpaceId(), ViewID: sch.GetViewId(), PrimaryDatasetID: sch.GetDatasetId(), ViewVersion: sch.GetViewVersion(), Engine: engineName, Columns: sch.GetColumns(), SchemaHash: sch.GetViewSchemaHash(), RowsPerSeries: sch.GetRowsPerSeries()}
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
	if runtime.active == req.GetIndexId() {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, fmt.Errorf("View 索引 %q 是活动索引，不能重新准备", req.GetIndexId()))}, nil
	}
	release, err := s.indexWriteGate(req.GetIndexId()).lock(ctx)
	if err != nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	defer release()
	// 上次没建完的半成品（含 DuckDB 的 WAL）先删干净。
	if err := engine.Remove(ctx, req.GetIndexId()); err != nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if err := engine.Prepare(ctx, req.GetIndexId(), schema); err != nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeIndexMappingsLocked(req.GetIndexId())
	s.indexEngine[req.GetIndexId()] = engineName
	s.schemas[req.GetIndexId()] = schema
	if s.catalogViews[viewKey] == nil {
		s.catalogViews[viewKey] = &pb.View{SpaceId: schema.SpaceID, ViewId: schema.ViewID, DatasetId: schema.PrimaryDatasetID, Engine: engineName}
	}
	if runtime.active == "" {
		runtime.building = req.GetIndexId()
	}
	s.indexView[req.GetIndexId()] = viewKey
	s.attachOwnedDatasetMappingLocked(sch.GetSpaceId(), sch.GetDatasetId(), req.GetIndexId())
	return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) ApplyViewIndex(ctx context.Context, req *pb.ApplyViewIndexReq) (*pb.ApplyViewIndexRsp, error) {
	if req == nil || req.GetBatch() == nil || req.GetIndexId() == "" {
		return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id and batch are required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	b := req.GetBatch()
	writes := make([]viewindex.RowWrite, 0, len(b.GetRowWrites()))
	for _, w := range b.GetRowWrites() {
		if w == nil || w.GetKey() == nil || w.GetKey().GetRowKey() == nil {
			return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("row key is required"))}, nil
		}
		writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: w.GetKey().GetRowKey()}, Fields: w.GetFields(), Attributes: w.GetAttributes()})
	}
	mode := viewindex.LiveWrite
	switch b.GetWriteMode() {
	case "BACKFILL":
		mode = viewindex.Backfill
	case "LIVE_WRITE":
	default:
		return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, fmt.Errorf("unsupported write mode %q", b.GetWriteMode()))}, nil
	}
	s.mu.RLock()
	viewKey, hasView := s.indexView[req.GetIndexId()]
	runtime := s.views[viewKey]
	if !hasView {
		runtime = nil
	}
	s.mu.RUnlock()
	if hasView && runtime != nil {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
	}
	engine, err := s.engineFor(req.GetIndexId())
	if err == nil {
		err = s.writeIndex(ctx, req.GetIndexId(), engine, viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: b.GetViewRevision(), ViewSchemaHash: b.GetViewSchemaHash(), WriteMode: mode})
	}
	if err != nil {
		return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) StatViewIndex(ctx context.Context, req *pb.StatViewIndexReq) (*pb.StatViewIndexRsp, error) {
	if req == nil || req.GetIndexId() == "" {
		return &pb.StatViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.StatViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	engine, err := s.engineFor(req.GetIndexId())
	if err != nil {
		return &pb.StatViewIndexRsp{RetInfo: retinfo.Error(queryErrorCode(err), err)}, nil
	}
	st, err := engine.Stat(ctx, req.GetIndexId())
	if err != nil {
		return &pb.StatViewIndexRsp{RetInfo: retinfo.Error(queryErrorCode(err), err)}, nil
	}
	return &pb.StatViewIndexRsp{RetInfo: retinfo.Success("success"), Stats: &pb.ViewIndexStats{Exists: st.Exists, EntryCount: uint64(st.EntryCount), ViewSchemaHash: st.SchemaHash, ViewVersion: st.ViewVersion, IndexedFrom: st.IndexedFrom, IndexedTo: st.IndexedTo, UpdatedAt: st.UpdatedAt}}, nil
}

func (s *Service) RemoveViewIndex(ctx context.Context, req *pb.RemoveViewIndexReq) (*pb.RemoveViewIndexRsp, error) {
	if req == nil || req.GetIndexId() == "" {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id 不能为空"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	indexID := req.GetIndexId()
	// 先取所属 View 的运行时锁、再取写锁（与挂接、清理的锁顺序一致）：删除与挂接、准备串行，不会删掉刚准备好或正在挂接的索引。
	runtime, release, err := s.lockIndexForRemoval(ctx, indexID)
	if err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	defer release()
	engine, err := s.engineFor(indexID)
	if err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	s.mu.RLock()
	engineName := normalizedEngine(s.indexEngine[indexID])
	s.mu.RUnlock()
	if err := engine.Remove(ctx, indexID); err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	s.mu.Lock()
	s.forgetIndexLocked(indexID)
	delete(s.cleanupCandidates, managedIndexRef{engine: engineName, indexID: indexID})
	s.mu.Unlock()
	// 运行时锁由 lockIndexForRemoval 持有：清掉指向该索引的 active 与 building。
	if runtime != nil {
		if runtime.active == indexID {
			runtime.clearActiveLocked()
		}
		if runtime.building == indexID {
			runtime.building = ""
		}
	}
	return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Success("success")}, nil
}

// maxRemovalLockAttempts 是 lockIndexForRemoval 在运行时于取锁期间被创建时的重试次数。
const maxRemovalLockAttempts = 3

// lockIndexForRemoval 按“运行时锁 → 写锁”的顺序锁住索引所属 View，返回运行时（可能为空）与释放函数。运行时是
// 首次准备索引时才创建的：取写锁期间它被创建了，就放开重来，保证总是先取运行时锁再取写锁。
func (s *Service) lockIndexForRemoval(ctx context.Context, indexID string) (*viewRuntime, func(), error) {
	for attempt := 0; attempt < maxRemovalLockAttempts; attempt++ {
		runtime := s.runtimeOfIndex(indexID)
		if runtime != nil {
			runtime.mu.Lock()
		}
		releaseGate, err := s.indexWriteGate(indexID).lock(ctx)
		if err != nil {
			if runtime != nil {
				runtime.mu.Unlock()
			}
			return nil, nil, err
		}
		if s.runtimeOfIndex(indexID) == runtime {
			return runtime, func() {
				releaseGate()
				if runtime != nil {
					runtime.mu.Unlock()
				}
			}, nil
		}
		releaseGate()
		if runtime != nil {
			runtime.mu.Unlock()
		}
	}
	return nil, nil, fmt.Errorf("View 索引 %q 所属的 View 正在初始化，请稍后重试", indexID)
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

func (s *Service) ListViewIndexes(ctx context.Context, req *pb.ListViewIndexesReq) (*pb.ListViewIndexesRsp, error) {
	if req == nil {
		return &pb.ListViewIndexesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.ListViewIndexesRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	s.mu.RLock()
	ids := make([]string, 0, len(s.catalogViews)*2)
	for key, view := range s.catalogViews {
		if req.GetSpaceId() != "" && req.GetSpaceId() != key.spaceID || req.GetViewId() != "" && req.GetViewId() != key.viewID {
			continue
		}
		if view.GetActiveIndexId() != "" {
			ids = append(ids, view.GetActiveIndexId())
		}
	}
	s.mu.RUnlock()
	ids = uniqueSorted(ids)
	sort.Strings(ids)
	out := make([]*pb.ViewIndexDescriptor, 0, len(ids))
	for _, id := range ids {
		engine, err := s.engineFor(id)
		if err != nil {
			continue
		}
		if req.GetEngine() != "" && !strings.EqualFold(req.GetEngine(), engine.Engine()) {
			continue
		}
		descriptor := &pb.ViewIndexDescriptor{IndexId: id, Engine: engine.Engine()}
		if req.GetIncludeStats() {
			st, _ := engine.Stat(ctx, id)
			descriptor.Stats = &pb.ViewIndexStats{Exists: st.Exists, EntryCount: uint64(st.EntryCount), ViewSchemaHash: st.SchemaHash, ViewVersion: st.ViewVersion, IndexedFrom: st.IndexedFrom, IndexedTo: st.IndexedTo, UpdatedAt: st.UpdatedAt}
		}
		out = append(out, descriptor)
	}
	return &pb.ListViewIndexesRsp{RetInfo: retinfo.Success("success"), Indexes: out}, nil
}

func uniqueSorted(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := ids[:0]
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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

// datasetHasActiveView distinguishes an unprojected Dataset (whose row event
// can be ACKed) from a managed Dataset whose View mapping is temporarily
// missing during discovery/recovery. The latter must remain pending so a
// newly-created View cannot miss rows before the next maintenance tick.
func (s *Service) datasetHasActiveView(ctx context.Context, spaceID, datasetID string) (bool, error) {
	metadata := s.metadataClientSnapshot()
	if metadata == nil {
		// Lightweight embedders may not configure MetadataClient. In production
		// the maintainer installs it before readiness is advertised; without it
		// there is no managed-view contract to protect and unrelated rows may ACK.
		return false, nil
	}
	auth := s.internalAuth()
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadata.ListViews(ctx, &pb.ListViewsReq{AuthInfo: auth, SpaceId: spaceID, Status: "active", Page: &pb.Page{Page: pageNo, Size: 100}})
		if err != nil {
			return false, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return false, err
		}
		for _, view := range rsp.GetViews() {
			if view == nil || view.GetSpaceId() != spaceID {
				continue
			}
			if strings.TrimSpace(view.GetDatasetId()) == datasetID {
				return true, nil
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			return false, nil
		}
	}
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
	s.removeIndexMappingsLocked(id)
	delete(s.indexEngine, id)
	delete(s.schemas, id)
	delete(s.indexView, id)
}

func (s *Service) removeIndexMappingsLocked(id string) {
	for ref, ids := range s.byData {
		delete(ids, id)
		if len(ids) == 0 {
			delete(s.byData, ref)
		}
	}
}

func (s *Service) authorize(auth *pb.AuthInfo) error {
	if s == nil || strings.TrimSpace(s.authSecret) == "" {
		return errors.New("view auth is not configured")
	}
	if auth == nil || strings.TrimSpace(auth.GetAppId()) == "" || strings.TrimSpace(auth.GetAppKey()) == "" {
		return errors.New("view auth is required")
	}
	expected := datanode.ServiceAuthKey(s.authSecret, auth.GetAppId())
	if !hmac.Equal([]byte(strings.ToLower(auth.GetAppKey())), []byte(expected)) {
		return errors.New("invalid view HMAC")
	}
	return nil
}

func viewDatasetIDs(view *pb.View) []string {
	if id := strings.TrimSpace(view.GetDatasetId()); id != "" {
		return []string{id}
	}
	return nil
}
