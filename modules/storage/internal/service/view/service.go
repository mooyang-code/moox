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

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	viewbleve "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/bleve"
	viewduckdb "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

type Service struct {
	engines      map[string]viewindex.Engine
	indexEngine  map[string]string
	schemas      map[string]viewindex.ViewIndexSchema
	views        map[viewRef]*viewRuntime
	catalogViews map[viewRef]*pb.View
	indexView    map[string]viewRef
	authSecret   string
	primaryAuth  *pb.AuthInfo
	primary      FieldReader
	mu           sync.RWMutex
	byData       map[datasetRef]map[string]struct{}
	liveWork     atomic.Int64
	liveGateOnce sync.Once
	liveGate     chan struct{}
}

type datasetRef struct{ spaceID, datasetID string }
type viewRef struct{ spaceID, viewID string }

type viewRuntime struct {
	mu           sync.Mutex
	active       string
	next         string
	status       string
	buildID      string
	ownerID      string
	metadata     MetadataClient
	metadataAuth *pb.AuthInfo
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
	if duckdbEngine, err := viewduckdb.OpenIndexManager(viewduckdb.IndexManagerOptions{Root: filepath.Join(root, "duckdb")}); err == nil {
		engines["duckdb"] = duckdbEngine
	}
	service := &Service{
		engines:      engines,
		indexEngine:  make(map[string]string),
		schemas:      make(map[string]viewindex.ViewIndexSchema),
		views:        make(map[viewRef]*viewRuntime),
		catalogViews: make(map[viewRef]*pb.View),
		indexView:    make(map[string]viewRef),
		authSecret:   authSecret,
		byData:       make(map[datasetRef]map[string]struct{}),
	}
	return service, nil
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
	schema := viewindex.ViewIndexSchema{SpaceID: sch.GetSpaceId(), ViewID: sch.GetViewId(), PrimaryDatasetID: sch.GetPrimaryDatasetId(), ViewVersion: sch.GetViewVersion(), Engine: engineName, Columns: sch.GetColumns(), SchemaHash: sch.GetViewSchemaHash()}
	err := engine.Prepare(ctx, req.GetIndexId(), schema)
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
	s.mu.Lock()
	s.removeIndexMappingsLocked(req.GetIndexId())
	s.indexEngine[req.GetIndexId()] = engineName
	s.schemas[req.GetIndexId()] = schema
	if runtime.active == "" {
		runtime.next = req.GetIndexId()
		runtime.status = "building"
	} else if runtime.active != req.GetIndexId() {
		runtime.next = req.GetIndexId()
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
	case "REPLACE":
		mode = viewindex.Replace
	}
	engine, err := s.engineFor(req.GetIndexId())
	if err == nil {
		err = engine.Write(ctx, req.GetIndexId(), viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: b.GetViewRevision(), ViewSchemaHash: b.GetViewSchemaHash(), WriteMode: mode})
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
	if err := engine.Remove(ctx, req.GetIndexId()); err != nil {
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

func (s *Service) removeFailedBuild(ctx context.Context, id string) {
	engine, err := s.engineFor(id)
	if err == nil {
		if err := engine.Remove(ctx, id); err != nil {
			log.Printf("storage view failed to remove index %q: %v", id, err)
		}
	} else {
		log.Printf("storage view failed to resolve index %q for removal: %v", id, err)
	}
	s.mu.Lock()
	s.removeIndexMappingsLocked(id)
	delete(s.indexEngine, id)
	delete(s.schemas, id)
	delete(s.indexView, id)
	s.mu.Unlock()
}

func (s *Service) engineFor(id string) (viewindex.Engine, error) {
	s.mu.RLock()
	name := s.indexEngine[id]
	s.mu.RUnlock()
	engine := s.engines[name]
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
