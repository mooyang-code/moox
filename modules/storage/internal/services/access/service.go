package access

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	"github.com/mooyang-code/moox/modules/storage/internal/core/schema"
	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/internal/services/primary"
	"github.com/mooyang-code/moox/modules/storage/internal/services/search"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	"trpc.group/trpc-go/trpc-go/log"
)

type Service struct {
	root       string
	duckDBPath string
	metadata   metadata.Store
	validator  *schema.Validator
	router     *router.Resolver
	primary    primary.Client
	search     *search.Service
	events     eventbus.Bus
	report     DerivedErrorReporter
}

var (
	_ pb.MetadataServiceService = (*Service)(nil)
	_ pb.DataServiceService     = (*Service)(nil)
	_ pb.QueryServiceService    = (*Service)(nil)
)

func NewService(root string) *Service {
	return NewServiceWithOptions(Options{Root: root})
}

func NewServiceWithOptions(opts Options) *Service {
	root := storageRoot(opts.Root)
	meta := opts.Metadata
	if meta == nil {
		var err error
		meta, err = openDefaultMetadataStore(context.Background(), root, opts.MetadataPath, opts.InitSchemaPath)
		if err != nil {
			panic(fmt.Sprintf("open storage metadata store: %v", err))
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
	reporter := opts.DerivedErrors
	if reporter == nil {
		reporter = logDerivedError
	}
	return &Service{
		root:       root,
		duckDBPath: opts.DuckDBPath,
		metadata:   meta,
		validator:  schema.NewValidator(meta),
		router:     router.NewResolver(meta),
		primary:    primaryClient,
		search: search.NewService(search.Options{
			Root:      root,
			BlevePath: opts.BlevePath,
			Metadata:  meta,
		}),
		events: events,
		report: reporter,
	}
}

var viewStores sync.Map

func (s *Service) viewStore() (*deviceduckdb.ViewStore, error) {
	path := s.duckDBPath
	if path == "" {
		path = filepath.Join(s.root, "duckdb", "views.duckdb")
	}
	if value, ok := viewStores.Load(path); ok {
		return value.(*deviceduckdb.ViewStore), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	store, err := deviceduckdb.Open(deviceduckdb.Options{Path: path})
	if err != nil {
		return nil, err
	}
	actual, loaded := viewStores.LoadOrStore(path, store)
	if loaded {
		_ = store.Close()
	}
	return actual.(*deviceduckdb.ViewStore), nil
}

func openDefaultMetadataStore(ctx context.Context, root string, metadataPath string, initSchemaPath string) (metadata.Store, error) {
	if metadataPath == "" {
		metadataPath = filepath.Join(root, "metadata", "storage_metadata.db")
	}
	metaDir := filepath.Dir(metadataPath)
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return nil, err
	}
	store, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       metadataPath,
		SchemaPath: initSchemaPath,
	})
	if err != nil {
		return nil, err
	}
	if initSchemaPath != "" {
		if err := store.InitSchema(ctx); err != nil {
			_ = store.Close()
			return nil, err
		}
		return store, nil
	}
	if err := requireMetadataSchema(ctx, store); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func requireMetadataSchema(ctx context.Context, store metadata.Store) error {
	tables, err := store.TableNames(ctx)
	if err != nil {
		return err
	}
	required := map[string]bool{
		"t_spaces":         false,
		"t_datasets":       false,
		"t_storage_routes": false,
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

func defaultSchemaPath() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return filepath.Join("schema", "storage_metadata.sql")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../schema/storage_metadata.sql"))
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

func logDerivedError(ctx context.Context, stage string, err error) {
	if err == nil {
		return
	}
	log.WarnContextf(ctx, "[StorageAccess] derived stage %s failed: %v", stage, err)
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
	if view.PrimaryDatasetId == "" && len(view.GetDatasetIds()) > 0 {
		view.PrimaryDatasetId = view.GetDatasetIds()[0]
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
	if view.PrimaryDatasetId == "" && len(view.GetDatasetIds()) > 0 {
		view.PrimaryDatasetId = view.GetDatasetIds()[0]
	}
	updated, err := s.metadata.UpsertView(ctx, view)
	if err != nil {
		return &pb.UpdateViewRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateViewRsp{RetInfo: response.Success("success"), View: updated}, nil
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

func (s *Service) CreateDataSet(ctx context.Context, req *pb.CreateDataSetReq) (*pb.CreateDataSetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetSpaceId() == "" || item.GetDataSourceId() == "" || (item.GetDatasetId() == "" && item.GetName() == "") {
		return &pb.CreateDataSetRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, data_source_id and dataset_id or name are required"))}, nil
	}
	if item.DatasetId == "" {
		item.DatasetId = defaultID(item.GetName(), "dataset")
	}
	if item.Name == "" {
		item.Name = item.GetDatasetId()
	}
	created, err := s.metadata.UpsertDataSet(ctx, item)
	if err != nil {
		return &pb.CreateDataSetRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateDataSetRsp{RetInfo: response.Success("success"), Dataset: created}, nil
}

func (s *Service) UpdateDataSet(ctx context.Context, req *pb.UpdateDataSetReq) (*pb.UpdateDataSetRsp, error) {
	updated, err := s.metadata.UpsertDataSet(ctx, req.GetDataset())
	if err != nil {
		return &pb.UpdateDataSetRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateDataSetRsp{RetInfo: response.Success("success"), Dataset: updated}, nil
}

func (s *Service) GetDataSet(ctx context.Context, req *pb.GetDataSetReq) (*pb.GetDataSetRsp, error) {
	item, err := s.metadata.GetDataSet(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.GetDataSetRsp{RetInfo: response.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
	}
	return &pb.GetDataSetRsp{RetInfo: response.Success("success"), Dataset: item}, nil
}

func (s *Service) ListDataSets(ctx context.Context, req *pb.ListDataSetsReq) (*pb.ListDataSetsRsp, error) {
	items, page, err := s.metadata.ListDataSets(ctx, req.GetSpaceId(), req.GetDataSourceId(), req.GetDataKind(), req.GetFreq(), req.GetPage())
	if err != nil {
		return &pb.ListDataSetsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDataSetsRsp{RetInfo: response.Success("success"), Datasets: items, PageResult: page}, nil
}

func (s *Service) BindDataSetSubject(ctx context.Context, req *pb.BindDataSetSubjectReq) (*pb.BindDataSetSubjectRsp, error) {
	item := req.GetDatasetSubject()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetSubjectId() == "" {
		return &pb.BindDataSetSubjectRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and subject_id are required"))}, nil
	}
	created, err := s.metadata.BindDataSetSubject(ctx, item)
	if err != nil {
		return &pb.BindDataSetSubjectRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.BindDataSetSubjectRsp{RetInfo: response.Success("success"), DatasetSubject: created}, nil
}

func (s *Service) ListDataSetSubjects(ctx context.Context, req *pb.ListDataSetSubjectsReq) (*pb.ListDataSetSubjectsRsp, error) {
	items, page, err := s.metadata.ListDataSetSubjectsPage(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSubjectId(), req.GetPage())
	if err != nil {
		return &pb.ListDataSetSubjectsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDataSetSubjectsRsp{RetInfo: response.Success("success"), DatasetSubjects: items, PageResult: page}, nil
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

func (s *Service) UpsertDataSetColumn(ctx context.Context, req *pb.UpsertDataSetColumnReq) (*pb.UpsertDataSetColumnRsp, error) {
	item := req.GetColumn()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return &pb.UpsertDataSetColumnRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and column_name are required"))}, nil
	}
	created, err := s.metadata.UpsertDataSetColumn(ctx, item)
	if err != nil {
		return &pb.UpsertDataSetColumnRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpsertDataSetColumnRsp{RetInfo: response.Success("success"), Column: created}, nil
}

func (s *Service) ListDataSetColumns(ctx context.Context, req *pb.ListDataSetColumnsReq) (*pb.ListDataSetColumnsRsp, error) {
	items, page, err := s.metadata.ListDataSetColumns(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetTextIndexedOnly(), req.GetPage())
	if err != nil {
		return &pb.ListDataSetColumnsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDataSetColumnsRsp{RetInfo: response.Success("success"), Columns: items, PageResult: page}, nil
}

func (s *Service) CreateStorageNode(ctx context.Context, req *pb.CreateStorageNodeReq) (*pb.CreateStorageNodeRsp, error) {
	item := req.GetNode()
	if item == nil || (item.GetNodeId() == "" && item.GetName() == "") {
		return &pb.CreateStorageNodeRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id or name is required"))}, nil
	}
	if item.NodeId == "" {
		item.NodeId = defaultID(item.GetName(), "node")
	}
	if item.Name == "" {
		item.Name = item.GetNodeId()
	}
	created, err := s.metadata.UpsertStorageNode(ctx, item)
	if err != nil {
		return &pb.CreateStorageNodeRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateStorageNodeRsp{RetInfo: response.Success("success"), Node: created}, nil
}

func (s *Service) UpdateStorageNode(ctx context.Context, req *pb.UpdateStorageNodeReq) (*pb.UpdateStorageNodeRsp, error) {
	updated, err := s.metadata.UpsertStorageNode(ctx, req.GetNode())
	if err != nil {
		return &pb.UpdateStorageNodeRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateStorageNodeRsp{RetInfo: response.Success("success"), Node: updated}, nil
}

func (s *Service) GetStorageNode(ctx context.Context, req *pb.GetStorageNodeReq) (*pb.GetStorageNodeRsp, error) {
	item, err := s.metadata.GetStorageNode(ctx, req.GetNodeId())
	if err != nil {
		return &pb.GetStorageNodeRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.GetStorageNodeRsp{RetInfo: response.Success("success"), Node: item}, nil
}

func (s *Service) ListStorageNodes(ctx context.Context, req *pb.ListStorageNodesReq) (*pb.ListStorageNodesRsp, error) {
	items, page, err := s.metadata.ListStorageNodes(ctx, req.GetPage())
	if err != nil {
		return &pb.ListStorageNodesRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListStorageNodesRsp{RetInfo: response.Success("success"), Nodes: items, PageResult: page}, nil
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

func (s *Service) CreateStorageRoute(ctx context.Context, req *pb.CreateStorageRouteReq) (*pb.CreateStorageRouteRsp, error) {
	item := req.GetStorageRoute()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetNodeId() == "" {
		return &pb.CreateStorageRouteRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and node_id are required"))}, nil
	}
	if item.RouteId == "" {
		item.RouteId = defaultID(strings.Join([]string{item.GetSpaceId(), item.GetDatasetId(), item.GetSubjectId(), item.GetNodeId()}, "-"), "route")
	}
	created, err := s.metadata.UpsertStorageRoute(ctx, item)
	if err != nil {
		return &pb.CreateStorageRouteRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateStorageRouteRsp{RetInfo: response.Success("success"), StorageRoute: created}, nil
}

func (s *Service) UpdateStorageRoute(ctx context.Context, req *pb.UpdateStorageRouteReq) (*pb.UpdateStorageRouteRsp, error) {
	updated, err := s.metadata.UpsertStorageRoute(ctx, req.GetStorageRoute())
	if err != nil {
		return &pb.UpdateStorageRouteRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateStorageRouteRsp{RetInfo: response.Success("success"), StorageRoute: updated}, nil
}

func (s *Service) GetStorageRoute(ctx context.Context, req *pb.GetStorageRouteReq) (*pb.GetStorageRouteRsp, error) {
	item, err := s.metadata.GetStorageRoute(ctx, req.GetSpaceId(), req.GetRouteId())
	if err != nil {
		return &pb.GetStorageRouteRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	return &pb.GetStorageRouteRsp{RetInfo: response.Success("success"), StorageRoute: item}, nil
}

func (s *Service) ListStorageRoutes(ctx context.Context, req *pb.ListStorageRoutesReq) (*pb.ListStorageRoutesRsp, error) {
	items, page, err := s.metadata.ListStorageRoutes(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSubjectId(), req.GetNodeId(), req.GetPage())
	if err != nil {
		return &pb.ListStorageRoutesRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListStorageRoutesRsp{RetInfo: response.Success("success"), StorageRoutes: items, PageResult: page}, nil
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
	return items[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint64(len(items)), HasMore: end < len(items)}
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
