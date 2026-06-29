package access

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	"github.com/mooyang-code/moox/modules/storage/internal/core/schema"
	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/internal/services/primary"
	"github.com/mooyang-code/moox/modules/storage/internal/services/search"
	"github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// Service 实现元数据、写入、权威读取和视图查询入口。
type Service struct {
	root                 string
	duckDBPath           string
	parquetPath          string
	metadata             metadata.Store
	metadataReader       metadata.Reader
	metadataCache        *metacache.Store
	validator            *schema.Validator
	router               *router.Resolver
	primary              primary.Client
	search               *search.Service
	factReader           factReadService
	timeSeriesFactReader view.FactReader
	viewFactReader       viewFactReadService
	events               eventbus.Bus
	report               ViewErrorReporter
	asyncMu              sync.Mutex
	asyncWG              sync.WaitGroup
	closing              bool
	viewDirtyMu          sync.Mutex
	viewDirtyBuilds      map[string]*viewDirtyBuild
	recordVersionMu      sync.Mutex
	lastRecordVersion    time.Time
	openedDuckDB         sync.Map
	viewStoresMu         sync.Mutex
	viewStores           map[string]*sharedViewStore
}

// factReadService 定义 Access 内部回读 Record 行所需的接口。
type factReadService interface {
	ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error)
}

// viewFactReadService 定义 View 查询/重建从 Access 读取事实行所需的接口。
type viewFactReadService interface {
	view.FactReader
	ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error)
	ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error)
}

var (
	_ pb.MetadataService = (*Service)(nil)
	_ pb.AccessService   = (*Service)(nil)
	_ pb.DataViewService = (*Service)(nil)
)

func newServiceViewStores() map[string]*sharedViewStore {
	return make(map[string]*sharedViewStore)
}

func NewServiceWithOptions(opts Options) *Service {
	root := storageRoot(opts.Root)
	meta := opts.Metadata
	reader := opts.MetadataReader
	var cacheReader *metacache.Store
	if meta == nil {
		var err error
		meta, cacheReader, err = openDefaultMetadataStores(trpc.BackgroundContext(), root, opts.MetadataPath, opts.InitSchemaPath)
		if err != nil {
			panic(fmt.Sprintf("open storage metadata store: %v", err))
		}
	}
	if reader == nil {
		if cacheReader != nil {
			reader = cacheReader
		} else {
			reader = meta
		}
	}
	primaryClient := opts.PrimaryClient
	if primaryClient == nil && opts.PrimaryServiceName != "" {
		primaryClient = primary.NewRemoteClient(opts.PrimaryServiceName)
	}
	if primaryClient == nil {
		primaryClient = primary.NewLocalClient(primary.LocalClientOptions{Root: root, PebblePath: opts.PebblePath})
	}
	events := opts.Events
	if events == nil {
		events = eventbus.NewMemoryBus()
	}
	reporter := opts.ViewErrors
	if reporter == nil {
		reporter = logViewError
	}
	svc := &Service{
		root:           root,
		duckDBPath:     opts.DuckDBPath,
		parquetPath:    opts.ParquetPath,
		metadata:       meta,
		metadataReader: reader,
		metadataCache:  cacheReader,
		validator:      schema.NewValidator(reader),
		router:         router.NewResolver(reader),
		primary:        primaryClient,
		search: search.NewService(search.Options{
			Root:      root,
			BlevePath: opts.BlevePath,
			Metadata:  reader,
		}),
		events: events,
		report: reporter,
		viewStores: newServiceViewStores(),
	}
	svc.factReader = svc
	svc.timeSeriesFactReader = svc
	svc.viewFactReader = svc
	return svc
}

func (s *Service) SetViewFactReader(reader viewFactReadService) {
	if s == nil || reader == nil {
		return
	}
	s.factReader = reader
	s.timeSeriesFactReader = reader
	s.viewFactReader = reader
}

// sharedViewStore 保存 DuckDB ViewStore 及其引用计数。
type sharedViewStore struct {
	store *deviceduckdb.ViewStore
	refs  int
}

func (s *Service) viewStore() (*deviceduckdb.ViewStore, error) {
	path := s.duckDBPath
	if path == "" {
		path = filepath.Join(s.root, "duckdb", "views.duckdb")
	}
	if _, ok := s.openedDuckDB.Load(path); ok {
		return s.getViewStore(path)
	}
	store, err := s.acquireViewStore(path)
	if err != nil {
		return nil, err
	}
	s.openedDuckDB.Store(path, struct{}{})
	return store, nil
}

func (s *Service) ViewStore() (*deviceduckdb.ViewStore, error) {
	if s == nil {
		return nil, errors.New("storage service is nil")
	}
	return s.viewStore()
}

func (s *Service) MetadataStore() metadata.Store {
	if s == nil {
		return nil
	}
	return s.metadata
}

func (s *Service) MetadataReader() metadata.Reader {
	if s == nil {
		return nil
	}
	return s.metadataReader
}

func (s *Service) SearchService() *search.Service {
	if s == nil {
		return nil
	}
	return s.search
}

// Close 释放本 Service 持有的派生资源，等待异步重建任务完成，并回收其打开的 DuckDB 视图存储。
// 用于优雅关闭，避免进程级全局缓存长期泄漏。
func (s *Service) Close() error {
	s.asyncMu.Lock()
	s.closing = true
	s.asyncMu.Unlock()
	var firstErr error
	s.waitForAsyncJobs()
	s.openedDuckDB.Range(func(key, _ any) bool {
		path, _ := key.(string)
		if err := s.releaseViewStore(path); err != nil && firstErr == nil {
			firstErr = err
		}
		s.openedDuckDB.Delete(key)
		return true
	})
	if s.search != nil {
		if err := s.search.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if closer, ok := s.events.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if closer, ok := s.primary.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.metadataCache != nil {
		if err := s.metadataCache.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if closer, ok := s.metadata.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Service) acquireViewStore(path string) (*deviceduckdb.ViewStore, error) {
	s.viewStoresMu.Lock()
	if shared := s.viewStores[path]; shared != nil {
		shared.refs++
		store := shared.store
		s.viewStoresMu.Unlock()
		return store, nil
	}
	s.viewStoresMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	opened, err := deviceduckdb.Open(deviceduckdb.Options{Path: path})
	if err != nil {
		return nil, err
	}

	s.viewStoresMu.Lock()
	defer s.viewStoresMu.Unlock()
	if shared := s.viewStores[path]; shared != nil {
		shared.refs++
		_ = opened.Close()
		return shared.store, nil
	}
	s.viewStores[path] = &sharedViewStore{store: opened, refs: 1}
	return opened, nil
}

func (s *Service) getViewStore(path string) (*deviceduckdb.ViewStore, error) {
	s.viewStoresMu.Lock()
	if shared := s.viewStores[path]; shared != nil {
		store := shared.store
		s.viewStoresMu.Unlock()
		return store, nil
	}
	s.viewStoresMu.Unlock()
	return s.acquireViewStore(path)
}

func (s *Service) releaseViewStore(path string) error {
	s.viewStoresMu.Lock()
	shared := s.viewStores[path]
	if shared == nil {
		s.viewStoresMu.Unlock()
		return nil
	}
	shared.refs--
	if shared.refs > 0 {
		s.viewStoresMu.Unlock()
		return nil
	}
	delete(s.viewStores, path)
	s.viewStoresMu.Unlock()
	return shared.store.Close()
}

func openDefaultMetadataStores(ctx context.Context, root string, metadataPath string, initSchemaPath string) (metadata.Store, *metacache.Store, error) {
	if metadataPath == "" {
		metadataPath = filepath.Join(root, "metadata", "storage_metadata.db")
	}
	metaDir := filepath.Dir(metadataPath)
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return nil, nil, err
	}
	store, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       metadataPath,
		SchemaPath: initSchemaPath,
	})
	if err != nil {
		return nil, nil, err
	}
	if initSchemaPath != "" {
		if err := store.InitSchema(ctx); err != nil {
			_ = store.Close()
			return nil, nil, err
		}
	} else if err := requireMetadataSchema(ctx, store); err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	cached, err := metacache.New(ctx, store, metacache.Options{})
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return store, cached, nil
}

func requireMetadataSchema(ctx context.Context, store metadata.Store) error {
	tables, err := store.TableNames(ctx)
	if err != nil {
		return err
	}
	required := map[string]bool{
		"t_spaces":               false,
		"t_datasets":             false,
		"t_primary_store_routes": false,
	}
	for _, table := range tables {
		if _, ok := required[table]; ok {
			required[table] = true
		}
	}
	var missing []string
	for table, exists := range required {
		if !exists {
			missing = append(missing, table)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("metadata schema not initialized, missing tables: %s", strings.Join(missing, ", "))
	}
	return nil
}

func storageRoot(root string) string {
	if root != "" {
		return root
	}
	if env := os.Getenv("MOOX_STORAGE_HOME"); env != "" {
		return env
	}
	return "var/storage"
}

func logViewError(ctx context.Context, stage string, err error) {
	if err == nil {
		return
	}
	log.WarnContextf(ctx, "[StorageAccess] view stage %s failed: %v", stage, err)
}
