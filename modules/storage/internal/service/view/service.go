package view

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	viewbleve "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/bleve"
	viewduckdb "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
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
	consumerState              func(context.Context) (jetstream.ConsumerState, error)
	consumerBound              func() bool
	consumerStates             map[string]func(context.Context) (jetstream.ConsumerState, error)
	consumerBounds             map[string]func() bool
	consumerPartitionByDataset map[datasetRef]string
	indexGatesMu               sync.Mutex
	indexGates                 map[string]*indexWriteGate
	indexGeneration            map[string]uint64
	retiringIndexes            map[string]struct{}
	rebuildMu                  sync.Mutex
	rebuildRunning             bool
	idleChecks                 map[viewRef]uint32
	rebuildLogRetry            map[string]pendingRebuildLog
	reconcileReady             bool
}

type pendingRebuildLog struct {
	opts     ReconcilerOptions
	auth     *pb.AuthInfo
	item     *pb.ViewRebuildLog
	view     *pb.View
	buildID  string
	result   pb.ViewRebuildResult
	entries  uint64
	cause    error
	fallback *pb.ViewRebuildLog
}

type datasetRef struct{ spaceID, datasetID string }
type viewRef struct{ spaceID, viewID string }

type viewRuntime struct {
	mu     sync.Mutex
	active string
	// activeDatasetIDs is the dataset contract of the currently readable
	// index. The metadata View carries the desired contract while an A/B
	// rebuild is in flight, so period events must not read DatasetIds from it.
	activeDatasetIDs       []string
	activeDatasetSet       bool
	activePrimaryDatasetID string
	statsIndexID           string
	stats                  viewindex.ViewIndexStats
	statsRefreshedAt       time.Time
	next                   string
	nextDatasetIDs         []string
	nextPrimaryDatasetID   string
	status                 string
	buildID                string
	ownerID                string
	metadata               MetadataClient
	metadataAuth           *pb.AuthInfo
	buildCancel            context.CancelFunc
	buildFailed            bool
	buildContext           context.Context
	lastSizeLimitBuildAt   time.Time
}

const (
	activeDatasetIDsAttr     = "moox.active_dataset_ids"
	activePrimaryDatasetAttr = "moox.active_primary_dataset_id"
)

func cloneViewAttributes(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	clone := make(map[string]string, len(attrs))
	for key, value := range attrs {
		clone[key] = value
	}
	return clone
}

func persistedActiveDatasetIDs(view *pb.View) []string {
	if view == nil {
		return nil
	}
	var ids []string
	if raw := strings.TrimSpace(view.GetAttributes()[activeDatasetIDsAttr]); raw != "" {
		_ = json.Unmarshal([]byte(raw), &ids)
	}
	return ids
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
		indexGeneration:            make(map[string]uint64),
		retiringIndexes:            make(map[string]struct{}),
		consumerStates:             make(map[string]func(context.Context) (jetstream.ConsumerState, error)),
		consumerBounds:             make(map[string]func() bool),
		consumerPartitionByDataset: make(map[datasetRef]string),
		idleChecks:                 make(map[viewRef]uint32),
		rebuildLogRetry:            make(map[string]pendingRebuildLog),
		metrics:                    observability.DefaultViewMetrics,
	}
	return service, nil
}

func (s *Service) markIndexRetiring(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	if s.retiringIndexes == nil {
		s.retiringIndexes = make(map[string]struct{})
	}
	s.retiringIndexes[id] = struct{}{}
	s.mu.Unlock()
}

func (s *Service) clearIndexRetiring(id string) {
	s.mu.Lock()
	delete(s.retiringIndexes, id)
	s.mu.Unlock()
}

func (s *Service) isIndexRetiring(id string) bool {
	s.mu.RLock()
	_, ok := s.retiringIndexes[id]
	s.mu.RUnlock()
	return ok
}

func (s *Service) setReconcileReady(ready bool) {
	s.mu.Lock()
	s.reconcileReady = ready
	s.mu.Unlock()
}

func (s *Service) isReconcileReady() bool {
	s.mu.RLock()
	ready := s.reconcileReady
	s.mu.RUnlock()
	return ready
}

func (s *Service) nextIndexGeneration(indexID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexGeneration[indexID]++
	return s.indexGeneration[indexID]
}

func (s *Service) indexGenerationOf(indexID string) uint64 {
	s.mu.RLock()
	generation := s.indexGeneration[indexID]
	s.mu.RUnlock()
	return generation
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
	release, err := s.indexWriteGate(indexID).lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	return engine.Write(ctx, indexID, batch)
}

func (s *Service) removeIndex(ctx context.Context, indexID string, engine viewindex.Engine) error {
	if engine == nil {
		return errors.New("view index engine is unavailable")
	}
	release, err := s.indexWriteGate(indexID).lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	return engine.Remove(ctx, indexID)
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
	if s.isIndexRetiring(req.GetIndexId()) {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, fmt.Errorf("view index %q is still in grace cleanup", req.GetIndexId()))}, nil
	}
	schema := viewindex.ViewIndexSchema{SpaceID: sch.GetSpaceId(), ViewID: sch.GetViewId(), PrimaryDatasetID: sch.GetPrimaryDatasetId(), ViewVersion: sch.GetViewVersion(), Engine: engineName, Columns: sch.GetColumns(), SchemaHash: sch.GetViewSchemaHash()}
	release, err := s.indexWriteGate(req.GetIndexId()).lock(ctx)
	if err != nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	// Advance the physical slot generation while holding the same gate used by
	// delayed old-slot removal. A grace cleanup that was already queued must
	// not delete this newly prepared generation.
	s.nextIndexGeneration(req.GetIndexId())
	err = engine.Prepare(ctx, req.GetIndexId(), schema)
	release()
	if err != nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	viewKey := viewRef{spaceID: schema.SpaceID, viewID: schema.ViewID}
	s.mu.Lock()
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = &viewRuntime{}
		s.views[viewKey] = runtime
	}
	s.mu.Unlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	refreshCatalog := runtime.active == ""
	if !refreshCatalog {
		s.mu.RLock()
		refreshCatalog = s.catalogViews[viewKey] == nil
		s.mu.RUnlock()
	}
	if !refreshCatalog && runtime.active != "" {
		activeEngine, activeErr := s.engineFor(runtime.active)
		if activeErr != nil {
			refreshCatalog = true
		} else if activeStats, statErr := activeEngine.Stat(ctx, runtime.active); statErr != nil || !activeStats.Exists {
			refreshCatalog = true
		}
	}
	s.mu.Lock()
	s.removeIndexMappingsLocked(req.GetIndexId())
	s.indexEngine[req.GetIndexId()] = engineName
	s.schemas[req.GetIndexId()] = schema
	if refreshCatalog {
		datasetIDs := append([]string(nil), sch.GetDatasetIds()...)
		seenDatasets := make(map[string]struct{})
		for _, datasetID := range datasetIDs {
			if datasetID != "" {
				seenDatasets[datasetID] = struct{}{}
			}
		}
		for _, column := range sch.GetColumns() {
			if datasetID := viewColumnDataset(column); datasetID != "" {
				if _, seen := seenDatasets[datasetID]; !seen {
					datasetIDs = append(datasetIDs, datasetID)
					seenDatasets[datasetID] = struct{}{}
				}
			}
		}
		if schema.PrimaryDatasetID != "" {
			if _, seen := seenDatasets[schema.PrimaryDatasetID]; !seen {
				datasetIDs = append(datasetIDs, schema.PrimaryDatasetID)
			}
		}
		columns := make([]*pb.ViewColumn, 0, len(sch.GetColumns()))
		for _, column := range sch.GetColumns() {
			if column != nil {
				columns = append(columns, proto.Clone(column).(*pb.ViewColumn))
			}
		}
		catalogView := s.catalogViews[viewKey]
		if catalogView == nil {
			catalogView = &pb.View{}
		} else {
			catalogView = proto.Clone(catalogView).(*pb.View)
		}
		catalogView.SpaceId = schema.SpaceID
		catalogView.ViewId = schema.ViewID
		catalogView.PrimaryDatasetId = schema.PrimaryDatasetID
		catalogView.DatasetIds = datasetIDs
		catalogView.Columns = columns
		if catalogView.Engine == "" {
			catalogView.Engine = engineName
		}
		s.catalogViews[viewKey] = catalogView
	}
	if runtime.active == "" {
		runtime.next = req.GetIndexId()
		runtime.nextDatasetIDs = append([]string(nil), sch.GetDatasetIds()...)
		runtime.nextPrimaryDatasetID = sch.GetPrimaryDatasetId()
		runtime.status = "building"
	} else if runtime.active != req.GetIndexId() {
		runtime.next = req.GetIndexId()
		runtime.nextDatasetIDs = append([]string(nil), sch.GetDatasetIds()...)
		runtime.nextPrimaryDatasetID = sch.GetPrimaryDatasetId()
		runtime.status = "building"
	}
	s.indexView[req.GetIndexId()] = viewKey
	for _, column := range sch.GetColumns() {
		dataset := viewColumnDataset(column)
		if dataset != "" {
			ref := datasetRef{spaceID: sch.GetSpaceId(), datasetID: dataset}
			if s.byData[ref] == nil {
				s.byData[ref] = make(map[string]struct{})
			}
			s.byData[ref][req.GetIndexId()] = struct{}{}
		}
	}
	s.mu.Unlock()
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
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	engine, err := s.engineFor(req.GetIndexId())
	if err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if err := s.removeIndex(ctx, req.GetIndexId(), engine); err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	s.mu.Lock()
	s.removeIndexMappingsLocked(req.GetIndexId())
	delete(s.indexEngine, req.GetIndexId())
	delete(s.schemas, req.GetIndexId())
	var runtime *viewRuntime
	if viewKey, ok := s.indexView[req.GetIndexId()]; ok {
		runtime = s.views[viewKey]
		delete(s.indexView, req.GetIndexId())
	}
	s.mu.Unlock()
	if runtime != nil {
		runtime.mu.Lock()
		if runtime.active == req.GetIndexId() {
			runtime.active = ""
		}
		if runtime.next == req.GetIndexId() {
			runtime.next = ""
		}
		runtime.mu.Unlock()
	}
	return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Success("success")}, nil
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
		if build := view.GetIndexBuild(); build != nil && build.GetIndexId() != "" {
			ids = append(ids, build.GetIndexId())
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
// newly-created View cannot miss rows before the next reconcile tick.
func (s *Service) datasetHasActiveView(ctx context.Context, spaceID, datasetID string) (bool, error) {
	metadata := s.metadataClientSnapshot()
	if metadata == nil {
		// Lightweight embedders may not configure MetadataClient. In production
		// the reconciler installs it before readiness is advertised; without it
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
			for _, projected := range view.GetDatasetIds() {
				if projected == datasetID {
					return true, nil
				}
			}
			for _, column := range view.GetColumns() {
				if viewColumnDataset(column) == datasetID {
					return true, nil
				}
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			return false, nil
		}
	}
}

func (s *Service) removeFailedBuild(ctx context.Context, id string) {
	s.removeFailedBuildAtGeneration(ctx, id, s.indexGenerationOf(id))
}

// removeFailedBuildAtGeneration prevents cleanup from deleting a newer
// incarnation of a reused A/B slot. PrepareViewIndex advances the generation
// while holding the same per-index gate, so checking it after acquiring that
// gate closes the gap between a failed build and its asynchronous cleanup.
func (s *Service) removeFailedBuildAtGeneration(ctx context.Context, id string, expectedGeneration uint64, engineOverride ...string) {
	// A failure cleanup must serialize with writers and pointer switches. The
	// runtime lock is acquired before the per-index gate, matching the live
	// write path (runtime -> gate), so a failed cleanup cannot remove a slot
	// after another goroutine has made that same slot active/next.
	s.mu.RLock()
	viewKey, mapped := s.indexView[id]
	runtime := s.views[viewKey]
	engineName := s.indexEngine[id]
	if engineName == "" && len(engineOverride) > 0 {
		engineName = strings.ToLower(strings.TrimSpace(engineOverride[0]))
	}
	engine := s.engines[engineName]
	s.mu.RUnlock()
	if mapped && runtime != nil {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
	}
	release, err := s.indexWriteGate(id).lock(ctx)
	if err != nil {
		return
	}
	defer release()
	s.mu.RLock()
	if s.indexGeneration[id] != expectedGeneration {
		s.mu.RUnlock()
		return
	}
	s.mu.RUnlock()
	if runtime != nil && (runtime.active == id || runtime.next == id) {
		return
	}
	if engine != nil {
		if err := engine.Remove(ctx, id); err != nil {
			log.Printf("storage view failed to remove index %q: %v", id, err)
			return
		}
	} else {
		log.Printf("storage view failed to resolve index %q for removal", id)
		return
	}
	s.mu.Lock()
	// Keep the mapping if a concurrent attach/prepare made this generation
	// authoritative while the engine removal was in flight.
	if runtime == nil || (runtime.active != id && runtime.next != id) {
		s.removeIndexMappingsLocked(id)
		delete(s.indexEngine, id)
		delete(s.schemas, id)
		delete(s.indexView, id)
	}
	s.mu.Unlock()
}

// removeIndexAfterGrace removes an old physical slot only when the slot has
// not been prepared again since the switch scheduled its cleanup. A/B slots
// are intentionally reused, so the generation check is required in addition
// to the per-index write gate.
func (s *Service) removeIndexAfterGrace(ctx context.Context, id string, expectedGeneration uint64) {
	if id == "" {
		return
	}
	release, err := s.indexWriteGate(id).lock(ctx)
	if err != nil {
		return
	}
	defer release()
	s.mu.RLock()
	currentGeneration := s.indexGeneration[id]
	engineName := s.indexEngine[id]
	engine := s.engines[engineName]
	s.mu.RUnlock()
	if currentGeneration != expectedGeneration || engine == nil {
		return
	}
	if err := engine.Remove(ctx, id); err != nil {
		log.Printf("storage view failed to remove old index %q: %v", id, err)
		go func() {
			time.Sleep(30 * time.Second)
			s.removeIndexAfterGrace(context.Background(), id, expectedGeneration)
		}()
		return
	}
	s.clearIndexRetiring(id)
	s.mu.Lock()
	if s.indexGeneration[id] == expectedGeneration {
		s.removeIndexMappingsLocked(id)
		delete(s.indexEngine, id)
		delete(s.schemas, id)
		delete(s.indexView, id)
	}
	s.mu.Unlock()
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
		runtime.mu.Lock()
		indexID := runtime.active
		runtime.mu.Unlock()
		return indexID, runtime
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
}

func cachedActiveIndexStats(runtime *viewRuntime, indexID string) (viewindex.ViewIndexStats, bool) {
	if runtime == nil || indexID == "" {
		return viewindex.ViewIndexStats{}, false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active != indexID || runtime.statsIndexID != indexID {
		return viewindex.ViewIndexStats{}, false
	}
	return runtime.stats, true
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
