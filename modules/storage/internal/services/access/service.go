package access

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	"github.com/mooyang-code/moox/modules/storage/internal/core/schema"
	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/internal/services/primary"
	"github.com/mooyang-code/moox/modules/storage/internal/services/search"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

var lowerSnakeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Service 实现元数据、写入、权威读取和视图查询入口。
type Service struct {
	root                     string
	duckDBPath               string
	parquetPath              string
	metadata                 metadata.Store
	metadataReader           metadata.Reader
	metadataCache            *metacache.Store
	validator                *schema.Validator
	router                   *router.Resolver
	primary                  primary.Client
	search                   *search.Service
	factReader               factReadService
	events                   eventbus.Bus
	report                   ViewErrorReporter
	indexMu                  sync.Mutex
	indexCond                *sync.Cond
	indexJobs                []indexJob
	indexWG                  sync.WaitGroup
	closing                  bool
	recordRowsChangedSub     eventbus.Subscription
	timeSeriesRowsChangedSub eventbus.Subscription
	viewDirtyMu              sync.Mutex
	viewDirtyBuilds          map[string]*viewDirtyBuild
	recordVersionMu          sync.Mutex
	lastRecordVersion        time.Time
	openedDuckDB             sync.Map
}

// factReadService 定义 Access 内部回读 Record 行所需的接口。
type factReadService interface {
	ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error)
}

var (
	_ pb.MetadataServiceService = (*Service)(nil)
	_ pb.AccessServiceService   = (*Service)(nil)
	_ pb.ViewServiceService     = (*Service)(nil)
)

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
	}
	svc.indexCond = sync.NewCond(&svc.indexMu)
	svc.factReader = svc
	go svc.runSearchIndexWorker()
	return svc
}

// StartEventConsumers 启动派生事件消费者。订阅失败会显式返回错误，
// 让服务启动阶段能发现 NATS subject / durable consumer 等配置问题。
func (s *Service) StartEventConsumers(ctx context.Context) error {
	subscriber, ok := s.events.(eventbus.Subscriber)
	if !ok {
		return nil
	}
	s.indexMu.Lock()
	if s.recordRowsChangedSub != nil || s.timeSeriesRowsChangedSub != nil {
		s.indexMu.Unlock()
		return nil
	}
	s.indexMu.Unlock()
	timeSeriesSubscription, err := subscriber.SubscribeTimeSeriesRowsChanged(ctx, s.handleTimeSeriesRowsChangedForView)
	if err != nil {
		return fmt.Errorf("subscribe time series rows changed: %w", err)
	}
	recordSubscription, err := subscriber.SubscribeRecordRowsChanged(ctx, s.handleRecordRowsChangedForSearch)
	if err != nil {
		_ = timeSeriesSubscription.Close()
		return fmt.Errorf("subscribe record rows changed: %w", err)
	}
	s.indexMu.Lock()
	if s.closing {
		s.indexMu.Unlock()
		_ = timeSeriesSubscription.Close()
		_ = recordSubscription.Close()
		return fmt.Errorf("subscribe rows changed: service is closing")
	}
	if s.recordRowsChangedSub != nil || s.timeSeriesRowsChangedSub != nil {
		s.indexMu.Unlock()
		_ = timeSeriesSubscription.Close()
		return recordSubscription.Close()
	}
	s.timeSeriesRowsChangedSub = timeSeriesSubscription
	s.recordRowsChangedSub = recordSubscription
	s.indexMu.Unlock()
	return nil
}

// sharedViewStore 保存进程内共享 DuckDB ViewStore 及其引用计数。
type sharedViewStore struct {
	store *deviceduckdb.ViewStore
	refs  int
}

var viewStores = struct {
	sync.Mutex
	items map[string]*sharedViewStore
}{items: make(map[string]*sharedViewStore)}

func (s *Service) viewStore() (*deviceduckdb.ViewStore, error) {
	path := s.duckDBPath
	if path == "" {
		path = filepath.Join(s.root, "duckdb", "views.duckdb")
	}
	if _, ok := s.openedDuckDB.Load(path); ok {
		return getViewStore(path)
	}
	store, err := acquireViewStore(path)
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

// Close 释放本 Service 持有的派生资源，等待异步索引完成，并回收其打开的 DuckDB 视图存储。
// 用于优雅关闭，避免进程级全局缓存长期泄漏。
func (s *Service) Close() error {
	s.indexMu.Lock()
	s.closing = true
	if s.indexCond != nil {
		s.indexCond.Broadcast()
	}
	recordRowsChangedSub := s.recordRowsChangedSub
	timeSeriesRowsChangedSub := s.timeSeriesRowsChangedSub
	s.recordRowsChangedSub = nil
	s.timeSeriesRowsChangedSub = nil
	s.indexMu.Unlock()
	var firstErr error
	if timeSeriesRowsChangedSub != nil {
		if err := timeSeriesRowsChangedSub.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if recordRowsChangedSub != nil {
		if err := recordRowsChangedSub.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.WaitForIndex()
	s.openedDuckDB.Range(func(key, _ any) bool {
		path, _ := key.(string)
		if err := releaseViewStore(path); err != nil && firstErr == nil {
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

func acquireViewStore(path string) (*deviceduckdb.ViewStore, error) {
	viewStores.Lock()
	if shared := viewStores.items[path]; shared != nil {
		shared.refs++
		store := shared.store
		viewStores.Unlock()
		return store, nil
	}
	viewStores.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	opened, err := deviceduckdb.Open(deviceduckdb.Options{Path: path})
	if err != nil {
		return nil, err
	}

	viewStores.Lock()
	defer viewStores.Unlock()
	if shared := viewStores.items[path]; shared != nil {
		shared.refs++
		_ = opened.Close()
		return shared.store, nil
	}
	viewStores.items[path] = &sharedViewStore{store: opened, refs: 1}
	return opened, nil
}

func getViewStore(path string) (*deviceduckdb.ViewStore, error) {
	viewStores.Lock()
	if shared := viewStores.items[path]; shared != nil {
		store := shared.store
		viewStores.Unlock()
		return store, nil
	}
	viewStores.Unlock()
	return acquireViewStore(path)
}

func releaseViewStore(path string) error {
	viewStores.Lock()
	shared := viewStores.items[path]
	if shared == nil {
		viewStores.Unlock()
		return nil
	}
	shared.refs--
	if shared.refs > 0 {
		viewStores.Unlock()
		return nil
	}
	delete(viewStores.items, path)
	viewStores.Unlock()
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

func (s *Service) CreateSpace(ctx context.Context, req *pb.CreateSpaceReq) (*pb.CreateSpaceRsp, error) {
	space := req.GetSpace()
	if space == nil || (space.GetSpaceId() == "" && space.GetName() == "") {
		return &pb.CreateSpaceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id or name is required"))}, nil
	}
	if space.SpaceId == "" {
		space.SpaceId = defaultID(space.GetName(), "space")
	}
	if space.Name == "" {
		space.Name = space.GetSpaceId()
	}
	created, err := s.metadata.UpsertSpace(ctx, space)
	if err != nil {
		return &pb.CreateSpaceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateSpaceRsp{RetInfo: response.Success("success"), Space: created}, nil
}

func (s *Service) UpdateSpace(ctx context.Context, req *pb.UpdateSpaceReq) (*pb.UpdateSpaceRsp, error) {
	space := req.GetSpace()
	if space == nil || space.GetSpaceId() == "" {
		return &pb.UpdateSpaceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id is required"))}, nil
	}
	if space.Name == "" {
		space.Name = space.GetSpaceId()
	}
	updated, err := s.metadata.UpsertSpace(ctx, space)
	if err != nil {
		return &pb.UpdateSpaceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateSpaceRsp{RetInfo: response.Success("success"), Space: updated}, nil
}

func (s *Service) GetSpace(ctx context.Context, req *pb.GetSpaceReq) (*pb.GetSpaceRsp, error) {
	space, err := s.metadata.GetSpace(ctx, req.GetSpaceId())
	if err != nil {
		return &pb.GetSpaceRsp{RetInfo: response.Error(pb.ErrorCode_SPACE_NOT_FOUND, err)}, nil
	}
	return &pb.GetSpaceRsp{RetInfo: response.Success("success"), Space: space}, nil
}

func (s *Service) ListSpaces(ctx context.Context, req *pb.ListSpacesReq) (*pb.ListSpacesRsp, error) {
	items, page, err := s.metadata.ListSpaces(ctx, req.GetOwner(), req.GetPage())
	if err != nil {
		return &pb.ListSpacesRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListSpacesRsp{RetInfo: response.Success("success"), Spaces: items, PageResult: page}, nil
}

func (s *Service) CreateView(ctx context.Context, req *pb.CreateViewReq) (*pb.CreateViewRsp, error) {
	view := req.GetView()
	if view == nil || view.GetSpaceId() == "" || (view.GetViewId() == "" && view.GetName() == "") {
		return &pb.CreateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id or name are required"))}, nil
	}
	if view.ViewId == "" {
		view.ViewId = defaultID(view.GetName(), "view")
	}
	if view.Name == "" {
		view.Name = view.GetViewId()
	}
	if err := validateViewID(view.GetViewId()); err != nil {
		return &pb.CreateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if view.PrimaryDatasetId == "" && len(view.GetDatasetIds()) > 0 {
		view.PrimaryDatasetId = view.GetDatasetIds()[0]
	}
	if err := s.normalizeAndValidateViewDatasets(ctx, view); err != nil {
		return &pb.CreateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	created, err := s.metadata.UpsertView(ctx, view)
	if err != nil {
		return &pb.CreateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateViewRsp{RetInfo: response.Success("success"), View: created}, nil
}

func (s *Service) UpdateView(ctx context.Context, req *pb.UpdateViewReq) (*pb.UpdateViewRsp, error) {
	view := req.GetView()
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
		return &pb.UpdateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id are required"))}, nil
	}
	if view.Name == "" {
		view.Name = view.GetViewId()
	}
	if err := validateViewID(view.GetViewId()); err != nil {
		return &pb.UpdateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if view.PrimaryDatasetId == "" && len(view.GetDatasetIds()) > 0 {
		view.PrimaryDatasetId = view.GetDatasetIds()[0]
	}
	if err := s.normalizeAndValidateViewDatasets(ctx, view); err != nil {
		return &pb.UpdateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	updated, err := s.metadata.UpsertView(ctx, view)
	if err != nil {
		return &pb.UpdateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateViewRsp{RetInfo: response.Success("success"), View: updated}, nil
}

func (s *Service) normalizeAndValidateViewDatasets(ctx context.Context, view *pb.View) error {
	if view == nil {
		return errors.New("view is required")
	}
	spaceID := strings.TrimSpace(view.GetSpaceId())
	primaryDatasetID := strings.TrimSpace(view.GetPrimaryDatasetId())
	if spaceID == "" || primaryDatasetID == "" {
		return errors.New("space_id and primary_dataset_id are required")
	}
	datasetIDs := normalizeViewDatasetIDs(primaryDatasetID, view.GetDatasetIds())
	var primary *pb.Dataset
	for idx, datasetID := range datasetIDs {
		dataset, err := s.metadata.GetDataset(ctx, spaceID, datasetID)
		if err != nil {
			return fmt.Errorf("view dataset %s not found: %w", datasetID, err)
		}
		if idx == 0 {
			primary = dataset
		}
	}
	if primary == nil {
		return errors.New("view datasets are required")
	}
	view.PrimaryDatasetId = primaryDatasetID
	view.DatasetIds = datasetIDs
	view.GrainKeys = defaultViewGrainKeys(primary.GetDataKind())
	view.Engine = defaultViewEngine(primary.GetDataKind())
	return nil
}

func defaultViewGrainKeys(kind pb.DataKind) []string {
	if kind == pb.DataKind_DATA_KIND_TIME_SERIES {
		return []string{"subject_id", "freq", "data_time"}
	}
	return []string{"record_id", "version"}
}

func defaultViewEngine(kind pb.DataKind) string {
	if kind == pb.DataKind_DATA_KIND_TIME_SERIES {
		return "duckdb"
	}
	return "bleve"
}

func validateDatasetID(datasetID string) error {
	return validateLowerSnakeID("dataset_id", datasetID, 20)
}

func validateViewID(viewID string) error {
	return validateLowerSnakeID("view_id", viewID, 30)
}

func validateLowerSnakeID(field string, value string, maxLen int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > maxLen {
		return fmt.Errorf("%s length must be <= %d", field, maxLen)
	}
	if !lowerSnakeIDPattern.MatchString(value) {
		return fmt.Errorf("%s must use lower snake case letters, digits and underscores", field)
	}
	return nil
}

func validateViewColumnName(column *pb.ViewColumn) error {
	if column.GetOriginType() != pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
		return nil
	}
	originID := strings.TrimSpace(column.GetOriginId())
	columnName := strings.TrimSpace(column.GetColumnName())
	datasetID, sourceName, ok := strings.Cut(originID, ".")
	if !ok || datasetID == "" || sourceName == "" {
		return errors.New("dataset view column origin_id must use dataset_id.column_name")
	}
	if err := validateDatasetID(datasetID); err != nil {
		return fmt.Errorf("invalid view column origin dataset: %w", err)
	}
	if columnName != originID {
		return errors.New("dataset view column column_name must equal origin_id and use dataset_id.column_name")
	}
	return nil
}

func normalizeViewDatasetIDs(primaryDatasetID string, datasetIDs []string) []string {
	seen := make(map[string]bool, len(datasetIDs)+1)
	out := make([]string, 0, len(datasetIDs)+1)
	add := func(datasetID string) {
		datasetID = strings.TrimSpace(datasetID)
		if datasetID == "" || seen[datasetID] {
			return
		}
		seen[datasetID] = true
		out = append(out, datasetID)
	}
	add(primaryDatasetID)
	for _, datasetID := range datasetIDs {
		add(datasetID)
	}
	return out
}

func (s *Service) GetView(ctx context.Context, req *pb.GetViewReq) (*pb.GetViewRsp, error) {
	view, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.GetViewRsp{RetInfo: response.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	return &pb.GetViewRsp{RetInfo: response.Success("success"), View: view}, nil
}

func (s *Service) ListViews(ctx context.Context, req *pb.ListViewsReq) (*pb.ListViewsRsp, error) {
	items, page, err := s.metadata.ListViews(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetStatus(), req.GetPage())
	if err != nil {
		return &pb.ListViewsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListViewsRsp{RetInfo: response.Success("success"), Views: items, PageResult: page}, nil
}

func (s *Service) UpsertViewColumn(ctx context.Context, req *pb.UpsertViewColumnReq) (*pb.UpsertViewColumnRsp, error) {
	column := req.GetColumn()
	if column == nil || column.GetSpaceId() == "" || column.GetViewId() == "" || column.GetColumnName() == "" {
		return &pb.UpsertViewColumnRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, view_id and column_name are required"))}, nil
	}
	if err := validateViewColumnName(column); err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	created, err := s.metadata.UpsertViewColumn(ctx, column)
	if err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpsertViewColumnRsp{RetInfo: response.Success("success"), Column: created}, nil
}

func (s *Service) ListViewColumns(ctx context.Context, req *pb.ListViewColumnsReq) (*pb.ListViewColumnsRsp, error) {
	items, page, err := s.metadata.ListViewColumns(ctx, req.GetSpaceId(), req.GetViewId(), req.GetPage())
	if err != nil {
		return &pb.ListViewColumnsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListViewColumnsRsp{RetInfo: response.Success("success"), Columns: items, PageResult: page}, nil
}

func (s *Service) CreateDataSource(ctx context.Context, req *pb.CreateDataSourceReq) (*pb.CreateDataSourceRsp, error) {
	item := req.GetDataSource()
	if item == nil || item.GetSpaceId() == "" || (item.GetDataSourceId() == "" && item.GetName() == "") {
		return &pb.CreateDataSourceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and data_source_id or name are required"))}, nil
	}
	if item.DataSourceId == "" {
		item.DataSourceId = defaultID(item.GetName(), "data_source")
	}
	if item.Name == "" {
		item.Name = item.GetDataSourceId()
	}
	created, err := s.metadata.UpsertDataSource(ctx, item)
	if err != nil {
		return &pb.CreateDataSourceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateDataSourceRsp{RetInfo: response.Success("success"), DataSource: created}, nil
}

func (s *Service) UpdateDataSource(ctx context.Context, req *pb.UpdateDataSourceReq) (*pb.UpdateDataSourceRsp, error) {
	updated, err := s.metadata.UpsertDataSource(ctx, req.GetDataSource())
	if err != nil {
		return &pb.UpdateDataSourceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateDataSourceRsp{RetInfo: response.Success("success"), DataSource: updated}, nil
}

func (s *Service) GetDataSource(ctx context.Context, req *pb.GetDataSourceReq) (*pb.GetDataSourceRsp, error) {
	item, err := s.metadata.GetDataSource(ctx, req.GetSpaceId(), req.GetDataSourceId())
	if err != nil {
		return &pb.GetDataSourceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.GetDataSourceRsp{RetInfo: response.Success("success"), DataSource: item}, nil
}

func (s *Service) ListDataSources(ctx context.Context, req *pb.ListDataSourcesReq) (*pb.ListDataSourcesRsp, error) {
	items, page, err := s.metadata.ListDataSources(ctx, req.GetSpaceId(), req.GetKind(), req.GetMarket(), req.GetPage())
	if err != nil {
		return &pb.ListDataSourcesRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDataSourcesRsp{RetInfo: response.Success("success"), DataSources: items, PageResult: page}, nil
}

func (s *Service) UpsertSubject(ctx context.Context, req *pb.UpsertSubjectReq) (*pb.UpsertSubjectRsp, error) {
	item := req.GetSubject()
	if item == nil || item.GetSpaceId() == "" || (item.GetSubjectId() == "" && item.GetName() == "") {
		return &pb.UpsertSubjectRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and subject_id or name are required"))}, nil
	}
	if item.SubjectId == "" {
		item.SubjectId = defaultID(item.GetName(), "subject")
	}
	if item.SubjectType == "" {
		item.SubjectType = "custom"
	}
	created, err := s.metadata.UpsertSubject(ctx, item)
	if err != nil {
		return &pb.UpsertSubjectRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpsertSubjectRsp{RetInfo: response.Success("success"), Subject: created}, nil
}

func (s *Service) GetSubject(ctx context.Context, req *pb.GetSubjectReq) (*pb.GetSubjectRsp, error) {
	item, err := s.metadata.GetSubject(ctx, req.GetSpaceId(), req.GetSubjectId())
	if err != nil {
		return &pb.GetSubjectRsp{RetInfo: response.Error(pb.ErrorCode_SUBJECT_NOT_FOUND, err)}, nil
	}
	return &pb.GetSubjectRsp{RetInfo: response.Success("success"), Subject: item}, nil
}

func (s *Service) ListSubjects(ctx context.Context, req *pb.ListSubjectsReq) (*pb.ListSubjectsRsp, error) {
	items, page, err := s.metadata.ListSubjects(ctx, req.GetSpaceId(), req.GetSubjectType(), req.GetMarket(), req.GetSubjectIds(), req.GetPage())
	if err != nil {
		return &pb.ListSubjectsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListSubjectsRsp{RetInfo: response.Success("success"), Subjects: items, PageResult: page}, nil
}

func (s *Service) UpsertSubjectSymbol(ctx context.Context, req *pb.UpsertSubjectSymbolReq) (*pb.UpsertSubjectSymbolRsp, error) {
	item := req.GetSubjectSymbol()
	if item == nil || item.GetSpaceId() == "" || item.GetSubjectId() == "" || item.GetDataSourceId() == "" || item.GetExternalSymbol() == "" {
		return &pb.UpsertSubjectSymbolRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, subject_id, data_source_id and external_symbol are required"))}, nil
	}
	created, err := s.metadata.UpsertSubjectSymbol(ctx, item)
	if err != nil {
		return &pb.UpsertSubjectSymbolRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpsertSubjectSymbolRsp{RetInfo: response.Success("success"), SubjectSymbol: created}, nil
}

func (s *Service) ListSubjectSymbols(ctx context.Context, req *pb.ListSubjectSymbolsReq) (*pb.ListSubjectSymbolsRsp, error) {
	items, page, err := s.metadata.ListSubjectSymbols(ctx, req.GetSpaceId(), req.GetSubjectId(), req.GetDataSourceId(), req.GetExternalSymbol(), req.GetPage())
	if err != nil {
		return &pb.ListSubjectSymbolsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListSubjectSymbolsRsp{RetInfo: response.Success("success"), SubjectSymbols: items, PageResult: page}, nil
}

func (s *Service) CreateDataset(ctx context.Context, req *pb.CreateDatasetReq) (*pb.CreateDatasetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetSpaceId() == "" || item.GetDataSourceId() == "" || (item.GetDatasetId() == "" && item.GetName() == "") {
		return &pb.CreateDatasetRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, data_source_id and dataset_id or name are required"))}, nil
	}
	if item.DatasetId == "" {
		item.DatasetId = defaultID(item.GetName(), "dataset")
	}
	if item.Name == "" {
		item.Name = item.GetDatasetId()
	}
	if err := validateDatasetID(item.GetDatasetId()); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	created, err := s.metadata.UpsertDataset(ctx, item)
	if err != nil {
		return &pb.CreateDatasetRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateDatasetRsp{RetInfo: response.Success("success"), Dataset: created}, nil
}

func (s *Service) UpdateDataset(ctx context.Context, req *pb.UpdateDatasetReq) (*pb.UpdateDatasetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetDatasetId() == "" {
		return &pb.UpdateDatasetRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset_id is required"))}, nil
	}
	if err := validateDatasetID(item.GetDatasetId()); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	updated, err := s.metadata.UpsertDataset(ctx, item)
	if err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateDatasetRsp{RetInfo: response.Success("success"), Dataset: updated}, nil
}

func (s *Service) GetDataset(ctx context.Context, req *pb.GetDatasetReq) (*pb.GetDatasetRsp, error) {
	item, err := s.metadata.GetDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.GetDatasetRsp{RetInfo: response.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
	}
	return &pb.GetDatasetRsp{RetInfo: response.Success("success"), Dataset: item}, nil
}

func (s *Service) ListDatasets(ctx context.Context, req *pb.ListDatasetsReq) (*pb.ListDatasetsRsp, error) {
	items, page, err := s.metadata.ListDatasets(ctx, req.GetSpaceId(), req.GetDataSourceId(), req.GetDataKind(), req.GetFreq(), req.GetPage())
	if err != nil {
		return &pb.ListDatasetsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDatasetsRsp{RetInfo: response.Success("success"), Datasets: items, PageResult: page}, nil
}

func (s *Service) BindDatasetSubject(ctx context.Context, req *pb.BindDatasetSubjectReq) (*pb.BindDatasetSubjectRsp, error) {
	item := req.GetDatasetSubject()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetSubjectId() == "" {
		return &pb.BindDatasetSubjectRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and subject_id are required"))}, nil
	}
	created, err := s.metadata.BindDatasetSubject(ctx, item)
	if err != nil {
		return &pb.BindDatasetSubjectRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.BindDatasetSubjectRsp{RetInfo: response.Success("success"), DatasetSubject: created}, nil
}

func (s *Service) ListDatasetSubjects(ctx context.Context, req *pb.ListDatasetSubjectsReq) (*pb.ListDatasetSubjectsRsp, error) {
	items, page, err := s.metadata.ListDatasetSubjects(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSubjectId(), req.GetPage())
	if err != nil {
		return &pb.ListDatasetSubjectsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDatasetSubjectsRsp{RetInfo: response.Success("success"), DatasetSubjects: items, PageResult: page}, nil
}

func (s *Service) CreateField(ctx context.Context, req *pb.CreateFieldReq) (*pb.CreateFieldRsp, error) {
	created, err := s.metadata.UpsertField(ctx, req.GetField())
	if err != nil {
		return &pb.CreateFieldRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateFieldRsp{RetInfo: response.Success("success"), Field: created}, nil
}

func (s *Service) UpdateField(ctx context.Context, req *pb.UpdateFieldReq) (*pb.UpdateFieldRsp, error) {
	updated, err := s.metadata.UpsertField(ctx, req.GetField())
	if err != nil {
		return &pb.UpdateFieldRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateFieldRsp{RetInfo: response.Success("success"), Field: updated}, nil
}

func (s *Service) GetField(ctx context.Context, req *pb.GetFieldReq) (*pb.GetFieldRsp, error) {
	item, err := s.metadata.GetField(ctx, req.GetSpaceId(), req.GetFieldId())
	if err != nil {
		return &pb.GetFieldRsp{RetInfo: response.Error(pb.ErrorCode_FIELD_NOT_FOUND, err)}, nil
	}
	return &pb.GetFieldRsp{RetInfo: response.Success("success"), Field: item}, nil
}

func (s *Service) ListFields(ctx context.Context, req *pb.ListFieldsReq) (*pb.ListFieldsRsp, error) {
	items, page, err := s.metadata.ListFields(ctx, req.GetSpaceId(), req.GetValueType(), req.GetPage())
	if err != nil {
		return &pb.ListFieldsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListFieldsRsp{RetInfo: response.Success("success"), Fields: items, PageResult: page}, nil
}

func (s *Service) CreateFactor(ctx context.Context, req *pb.CreateFactorReq) (*pb.CreateFactorRsp, error) {
	created, err := s.metadata.UpsertFactor(ctx, req.GetFactor())
	if err != nil {
		return &pb.CreateFactorRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateFactorRsp{RetInfo: response.Success("success"), Factor: created}, nil
}

func (s *Service) UpdateFactor(ctx context.Context, req *pb.UpdateFactorReq) (*pb.UpdateFactorRsp, error) {
	updated, err := s.metadata.UpsertFactor(ctx, req.GetFactor())
	if err != nil {
		return &pb.UpdateFactorRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateFactorRsp{RetInfo: response.Success("success"), Factor: updated}, nil
}

func (s *Service) GetFactor(ctx context.Context, req *pb.GetFactorReq) (*pb.GetFactorRsp, error) {
	item, err := s.metadata.GetFactor(ctx, req.GetSpaceId(), req.GetFactorId())
	if err != nil {
		return &pb.GetFactorRsp{RetInfo: response.Error(pb.ErrorCode_FACTOR_NOT_FOUND, err)}, nil
	}
	return &pb.GetFactorRsp{RetInfo: response.Success("success"), Factor: item}, nil
}

func (s *Service) ListFactors(ctx context.Context, req *pb.ListFactorsReq) (*pb.ListFactorsRsp, error) {
	items, page, err := s.metadata.ListFactors(ctx, req.GetSpaceId(), req.GetAlgorithm(), req.GetPage())
	if err != nil {
		return &pb.ListFactorsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListFactorsRsp{RetInfo: response.Success("success"), Factors: items, PageResult: page}, nil
}

func (s *Service) UpsertDatasetColumn(ctx context.Context, req *pb.UpsertDatasetColumnReq) (*pb.UpsertDatasetColumnRsp, error) {
	item := req.GetColumn()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return &pb.UpsertDatasetColumnRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and column_name are required"))}, nil
	}
	created, err := s.metadata.UpsertDatasetColumn(ctx, item)
	if err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpsertDatasetColumnRsp{RetInfo: response.Success("success"), Column: created}, nil
}

func (s *Service) ListDatasetColumns(ctx context.Context, req *pb.ListDatasetColumnsReq) (*pb.ListDatasetColumnsRsp, error) {
	items, page, err := s.metadata.ListDatasetColumns(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetPage())
	if err != nil {
		return &pb.ListDatasetColumnsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDatasetColumnsRsp{RetInfo: response.Success("success"), Columns: items, PageResult: page}, nil
}

func (s *Service) CreatePrimaryStoreNode(ctx context.Context, req *pb.CreatePrimaryStoreNodeReq) (*pb.CreatePrimaryStoreNodeRsp, error) {
	item := req.GetNode()
	if item == nil || (item.GetNodeId() == "" && item.GetName() == "") {
		return &pb.CreatePrimaryStoreNodeRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id or name is required"))}, nil
	}
	if item.NodeId == "" {
		item.NodeId = defaultID(item.GetName(), "node")
	}
	if item.Name == "" {
		item.Name = item.GetNodeId()
	}
	created, err := s.metadata.UpsertPrimaryStoreNode(ctx, item)
	if err != nil {
		return &pb.CreatePrimaryStoreNodeRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreatePrimaryStoreNodeRsp{RetInfo: response.Success("success"), Node: created}, nil
}

func (s *Service) UpdatePrimaryStoreNode(ctx context.Context, req *pb.UpdatePrimaryStoreNodeReq) (*pb.UpdatePrimaryStoreNodeRsp, error) {
	updated, err := s.metadata.UpsertPrimaryStoreNode(ctx, req.GetNode())
	if err != nil {
		return &pb.UpdatePrimaryStoreNodeRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdatePrimaryStoreNodeRsp{RetInfo: response.Success("success"), Node: updated}, nil
}

func (s *Service) GetPrimaryStoreNode(ctx context.Context, req *pb.GetPrimaryStoreNodeReq) (*pb.GetPrimaryStoreNodeRsp, error) {
	item, err := s.metadata.GetPrimaryStoreNode(ctx, req.GetNodeId())
	if err != nil {
		return &pb.GetPrimaryStoreNodeRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.GetPrimaryStoreNodeRsp{RetInfo: response.Success("success"), Node: item}, nil
}

func (s *Service) ListPrimaryStoreNodes(ctx context.Context, req *pb.ListPrimaryStoreNodesReq) (*pb.ListPrimaryStoreNodesRsp, error) {
	items, page, err := s.metadata.ListPrimaryStoreNodes(ctx, req.GetPage())
	if err != nil {
		return &pb.ListPrimaryStoreNodesRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListPrimaryStoreNodesRsp{RetInfo: response.Success("success"), Nodes: items, PageResult: page}, nil
}

func (s *Service) CreateDevice(ctx context.Context, req *pb.CreateDeviceReq) (*pb.CreateDeviceRsp, error) {
	item := req.GetDevice()
	if item == nil || (item.GetDeviceId() == "" && item.GetName() == "") {
		return &pb.CreateDeviceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("device_id or name is required"))}, nil
	}
	if item.DeviceId == "" {
		item.DeviceId = defaultID(item.GetName(), "device")
	}
	if item.Name == "" {
		item.Name = item.GetDeviceId()
	}
	created, err := s.metadata.UpsertDevice(ctx, item)
	if err != nil {
		return &pb.CreateDeviceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateDeviceRsp{RetInfo: response.Success("success"), Device: created}, nil
}

func (s *Service) UpdateDevice(ctx context.Context, req *pb.UpdateDeviceReq) (*pb.UpdateDeviceRsp, error) {
	updated, err := s.metadata.UpsertDevice(ctx, req.GetDevice())
	if err != nil {
		return &pb.UpdateDeviceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateDeviceRsp{RetInfo: response.Success("success"), Device: updated}, nil
}

func (s *Service) GetDevice(ctx context.Context, req *pb.GetDeviceReq) (*pb.GetDeviceRsp, error) {
	item, err := s.metadata.GetDevice(ctx, req.GetDeviceId())
	if err != nil {
		return &pb.GetDeviceRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.GetDeviceRsp{RetInfo: response.Success("success"), Device: item}, nil
}

func (s *Service) ListDevices(ctx context.Context, req *pb.ListDevicesReq) (*pb.ListDevicesRsp, error) {
	items, page, err := s.metadata.ListDevices(ctx, req.GetNodeId(), req.GetEngine(), req.GetPage())
	if err != nil {
		return &pb.ListDevicesRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDevicesRsp{RetInfo: response.Success("success"), Devices: items, PageResult: page}, nil
}

func (s *Service) CreatePrimaryStoreRoute(ctx context.Context, req *pb.CreatePrimaryStoreRouteReq) (*pb.CreatePrimaryStoreRouteRsp, error) {
	item := req.GetPrimaryStoreRoute()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetNodeId() == "" {
		return &pb.CreatePrimaryStoreRouteRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and node_id are required"))}, nil
	}
	if item.RouteId == "" {
		item.RouteId = defaultID(strings.Join([]string{item.GetSpaceId(), item.GetDatasetId(), item.GetSubjectId(), item.GetNodeId()}, "-"), "route")
	}
	created, err := s.metadata.UpsertPrimaryStoreRoute(ctx, item)
	if err != nil {
		return &pb.CreatePrimaryStoreRouteRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreatePrimaryStoreRouteRsp{RetInfo: response.Success("success"), PrimaryStoreRoute: created}, nil
}

func (s *Service) UpdatePrimaryStoreRoute(ctx context.Context, req *pb.UpdatePrimaryStoreRouteReq) (*pb.UpdatePrimaryStoreRouteRsp, error) {
	updated, err := s.metadata.UpsertPrimaryStoreRoute(ctx, req.GetPrimaryStoreRoute())
	if err != nil {
		return &pb.UpdatePrimaryStoreRouteRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdatePrimaryStoreRouteRsp{RetInfo: response.Success("success"), PrimaryStoreRoute: updated}, nil
}

func (s *Service) GetPrimaryStoreRoute(ctx context.Context, req *pb.GetPrimaryStoreRouteReq) (*pb.GetPrimaryStoreRouteRsp, error) {
	item, err := s.metadata.GetPrimaryStoreRoute(ctx, req.GetSpaceId(), req.GetRouteId())
	if err != nil {
		return &pb.GetPrimaryStoreRouteRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	return &pb.GetPrimaryStoreRouteRsp{RetInfo: response.Success("success"), PrimaryStoreRoute: item}, nil
}

func (s *Service) ListPrimaryStoreRoutes(ctx context.Context, req *pb.ListPrimaryStoreRoutesReq) (*pb.ListPrimaryStoreRoutesRsp, error) {
	items, page, err := s.metadata.ListPrimaryStoreRoutes(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSubjectId(), req.GetNodeId(), req.GetPage())
	if err != nil {
		return &pb.ListPrimaryStoreRoutesRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListPrimaryStoreRoutesRsp{RetInfo: response.Success("success"), PrimaryStoreRoutes: items, PageResult: page}, nil
}

func (s *Service) RegisterArchiveFile(ctx context.Context, req *pb.RegisterArchiveFileReq) (*pb.RegisterArchiveFileRsp, error) {
	item := req.GetArchiveFile()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetDeviceId() == "" || item.GetFileUri() == "" {
		return &pb.RegisterArchiveFileRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id, device_id and file_uri are required"))}, nil
	}
	if item.ArchiveFileId == "" {
		item.ArchiveFileId = defaultID(strings.Join([]string{item.GetSpaceId(), item.GetDatasetId(), item.GetPartitionKey(), item.GetFileUri()}, "-"), "archive_file")
	}
	created, err := s.metadata.RegisterArchiveFile(ctx, item)
	if err != nil {
		return &pb.RegisterArchiveFileRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.RegisterArchiveFileRsp{RetInfo: response.Success("success"), ArchiveFile: created}, nil
}

func (s *Service) ListArchiveFiles(ctx context.Context, req *pb.ListArchiveFilesReq) (*pb.ListArchiveFilesRsp, error) {
	items, _, err := s.metadata.ListArchiveFiles(ctx, req.GetSpaceId(), req.GetDatasetId(), nil)
	if err != nil {
		return &pb.ListArchiveFilesRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	filtered := items[:0]
	for _, item := range items {
		if req.GetDeviceId() != "" && item.GetDeviceId() != req.GetDeviceId() {
			continue
		}
		if req.GetPartitionKey() != "" && item.GetPartitionKey() != req.GetPartitionKey() {
			continue
		}
		if !archiveFileOverlaps(item, req.GetTimeRange()) {
			continue
		}
		filtered = append(filtered, item)
	}
	paged, page := pageSlice(filtered, req.GetPage())
	return &pb.ListArchiveFilesRsp{RetInfo: response.Success("success"), ArchiveFiles: paged, PageResult: page}, nil
}

func defaultID(name, prefix string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return prefix + "_" + xid.New().String()
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_")
	return replacer.Replace(name)
}

func pageSlice[T any](items []T, page *pb.Page) ([]T, *pb.PageResult) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	start := int((pageNo - 1) * size)
	if start > len(items) {
		start = len(items)
	}
	end := start + int(size)
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(items)), HasMore: end < len(items)}
}

func archiveFileOverlaps(item *pb.ArchiveFile, timeRange *pb.TimeRange) bool {
	if timeRange == nil {
		return true
	}
	if timeRange.GetStartTime() != "" && item.GetMaxTime() != "" && item.GetMaxTime() < timeRange.GetStartTime() {
		return false
	}
	if timeRange.GetEndTime() != "" && item.GetMinTime() != "" && item.GetMinTime() > timeRange.GetEndTime() {
		return false
	}
	return true
}
