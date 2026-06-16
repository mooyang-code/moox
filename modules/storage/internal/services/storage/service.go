package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter"
	"github.com/mooyang-code/moox/modules/storage/internal/services/changefeed"
	devicebleve "github.com/mooyang-code/moox/modules/storage/internal/services/device/bleve"
	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/services/device/duckdb"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/services/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/internal/services/router"
	"github.com/mooyang-code/moox/modules/storage/internal/services/schema"
	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
)

type Service struct {
	store     *quantstore.Store
	root      string
	metadata  metadata.Store
	validator *schema.Validator
	router    *router.Resolver
	adapter   adapter.Client
	changes   changefeed.Publisher
}

var (
	_ pb.MetadataServiceService = (*Service)(nil)
	_ pb.DataServiceService     = (*Service)(nil)
	_ pb.QueryServiceService    = (*Service)(nil)
	_ pb.AdapterServiceService  = (*Service)(nil)
)

func NewService(root string) *Service {
	return NewServiceWithOptions(Options{Root: root})
}

func NewServiceWithOptions(opts Options) *Service {
	root := storageRoot(opts.Root)
	meta := opts.Metadata
	if meta == nil {
		var err error
		meta, err = openDefaultMetadataStore(context.Background(), root, opts.MetadataPath, opts.SchemaPath)
		if err != nil {
			panic(fmt.Sprintf("open storage metadata store: %v", err))
		}
	}
	store := quantstore.New(root)
	return &Service{
		store:     store,
		root:      root,
		metadata:  meta,
		validator: schema.NewValidator(meta),
		router:    router.NewResolver(meta),
		adapter:   adapter.NewLocalClient(store),
		changes:   changefeed.NewMemoryPublisher(),
	}
}

var searchIndexes sync.Map
var viewStores sync.Map

func (s *Service) searchIndex() (*devicebleve.Index, error) {
	path := filepath.Join(s.root, "bleve", "default")
	if value, ok := searchIndexes.Load(path); ok {
		return value.(*devicebleve.Index), nil
	}
	index, err := devicebleve.Open(devicebleve.Options{Path: path})
	if err != nil {
		return nil, err
	}
	actual, loaded := searchIndexes.LoadOrStore(path, index)
	if loaded {
		_ = index.Close()
	}
	return actual.(*devicebleve.Index), nil
}

func (s *Service) viewStore() (*deviceduckdb.ViewStore, error) {
	path := filepath.Join(s.root, "duckdb", "views.duckdb")
	if value, ok := viewStores.Load(path); ok {
		return value.(*deviceduckdb.ViewStore), nil
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

func openDefaultMetadataStore(ctx context.Context, root string, metadataPath string, schemaPath string) (metadata.Store, error) {
	if metadataPath == "" {
		metadataPath = filepath.Join(root, "metadata", "storage_metadata.db")
	}
	if schemaPath == "" {
		schemaPath = defaultSchemaPath()
	}
	metaDir := filepath.Dir(metadataPath)
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return nil, err
	}
	store, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       metadataPath,
		SchemaPath: schemaPath,
	})
	if err != nil {
		return nil, err
	}
	if err := store.InitSchema(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func defaultSchemaPath() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return filepath.Join("schema", "storage_metadata.sql")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../schema/storage_metadata.sql"))
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

func (s *Service) CreateSpace(ctx context.Context, req *pb.CreateSpaceReq) (*pb.CreateSpaceRsp, error) {
	space := req.GetSpace()
	if space == nil || (space.GetSpaceId() == "" && space.GetName() == "") {
		return &pb.CreateSpaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id or name is required"))}, nil
	}
	if space.SpaceId == "" {
		space.SpaceId = defaultID(space.GetName(), "space")
	}
	if space.Name == "" {
		space.Name = space.GetSpaceId()
	}
	created, err := s.metadata.UpsertSpace(ctx, space)
	if err != nil {
		return &pb.CreateSpaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateSpaceRsp{RetInfo: quantstore.Success("success"), Space: created}, nil
}

func (s *Service) UpdateSpace(ctx context.Context, req *pb.UpdateSpaceReq) (*pb.UpdateSpaceRsp, error) {
	space := req.GetSpace()
	if space == nil || space.GetSpaceId() == "" {
		return &pb.UpdateSpaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id is required"))}, nil
	}
	if space.Name == "" {
		space.Name = space.GetSpaceId()
	}
	updated, err := s.metadata.UpsertSpace(ctx, space)
	if err != nil {
		return &pb.UpdateSpaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateSpaceRsp{RetInfo: quantstore.Success("success"), Space: updated}, nil
}

func (s *Service) GetSpace(ctx context.Context, req *pb.GetSpaceReq) (*pb.GetSpaceRsp, error) {
	space, err := s.metadata.GetSpace(ctx, req.GetSpaceId())
	if err != nil {
		return &pb.GetSpaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_SPACE_NOT_FOUND, err)}, nil
	}
	return &pb.GetSpaceRsp{RetInfo: quantstore.Success("success"), Space: space}, nil
}

func (s *Service) ListSpaces(ctx context.Context, req *pb.ListSpacesReq) (*pb.ListSpacesRsp, error) {
	items, page, err := s.metadata.ListSpaces(ctx, req.GetOwner(), req.GetPage())
	if err != nil {
		return &pb.ListSpacesRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListSpacesRsp{RetInfo: quantstore.Success("success"), Spaces: items, PageResult: page}, nil
}

func (s *Service) CreateView(ctx context.Context, req *pb.CreateViewReq) (*pb.CreateViewRsp, error) {
	view := req.GetView()
	if view == nil || view.GetSpaceId() == "" || (view.GetViewId() == "" && view.GetName() == "") {
		return &pb.CreateViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id or name are required"))}, nil
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
		return &pb.CreateViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateViewRsp{RetInfo: quantstore.Success("success"), View: created}, nil
}

func (s *Service) UpdateView(ctx context.Context, req *pb.UpdateViewReq) (*pb.UpdateViewRsp, error) {
	view := req.GetView()
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
		return &pb.UpdateViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id are required"))}, nil
	}
	if view.Name == "" {
		view.Name = view.GetViewId()
	}
	if view.PrimaryDatasetId == "" && len(view.GetDatasetIds()) > 0 {
		view.PrimaryDatasetId = view.GetDatasetIds()[0]
	}
	updated, err := s.metadata.UpsertView(ctx, view)
	if err != nil {
		return &pb.UpdateViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateViewRsp{RetInfo: quantstore.Success("success"), View: updated}, nil
}

func (s *Service) GetView(ctx context.Context, req *pb.GetViewReq) (*pb.GetViewRsp, error) {
	view, err := s.metadata.GetView(ctx, req.GetSpaceId(), req.GetViewId())
	if err != nil {
		return &pb.GetViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_VIEW_NOT_FOUND, err)}, nil
	}
	return &pb.GetViewRsp{RetInfo: quantstore.Success("success"), View: view}, nil
}

func (s *Service) ListViews(ctx context.Context, req *pb.ListViewsReq) (*pb.ListViewsRsp, error) {
	items, page, err := s.metadata.ListViews(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetStatus(), req.GetPage())
	if err != nil {
		return &pb.ListViewsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListViewsRsp{RetInfo: quantstore.Success("success"), Views: items, PageResult: page}, nil
}

func (s *Service) UpsertViewColumn(ctx context.Context, req *pb.UpsertViewColumnReq) (*pb.UpsertViewColumnRsp, error) {
	column := req.GetColumn()
	if column == nil || column.GetSpaceId() == "" || column.GetViewId() == "" || column.GetColumnName() == "" {
		return &pb.UpsertViewColumnRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, view_id and column_name are required"))}, nil
	}
	created, err := s.metadata.UpsertViewColumn(ctx, column)
	if err != nil {
		return &pb.UpsertViewColumnRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpsertViewColumnRsp{RetInfo: quantstore.Success("success"), Column: created}, nil
}

func (s *Service) ListViewColumns(ctx context.Context, req *pb.ListViewColumnsReq) (*pb.ListViewColumnsRsp, error) {
	items, page, err := s.metadata.ListViewColumns(ctx, req.GetSpaceId(), req.GetViewId(), req.GetPage())
	if err != nil {
		return &pb.ListViewColumnsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListViewColumnsRsp{RetInfo: quantstore.Success("success"), Columns: items, PageResult: page}, nil
}

func (s *Service) CreateDataSource(ctx context.Context, req *pb.CreateDataSourceReq) (*pb.CreateDataSourceRsp, error) {
	item := req.GetDataSource()
	if item == nil || item.GetSpaceId() == "" || (item.GetDataSourceId() == "" && item.GetName() == "") {
		return &pb.CreateDataSourceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and data_source_id or name are required"))}, nil
	}
	if item.DataSourceId == "" {
		item.DataSourceId = defaultID(item.GetName(), "data_source")
	}
	if item.Name == "" {
		item.Name = item.GetDataSourceId()
	}
	created, err := s.metadata.UpsertDataSource(ctx, item)
	if err != nil {
		return &pb.CreateDataSourceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateDataSourceRsp{RetInfo: quantstore.Success("success"), DataSource: created}, nil
}

func (s *Service) UpdateDataSource(ctx context.Context, req *pb.UpdateDataSourceReq) (*pb.UpdateDataSourceRsp, error) {
	updated, err := s.metadata.UpsertDataSource(ctx, req.GetDataSource())
	if err != nil {
		return &pb.UpdateDataSourceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateDataSourceRsp{RetInfo: quantstore.Success("success"), DataSource: updated}, nil
}

func (s *Service) GetDataSource(ctx context.Context, req *pb.GetDataSourceReq) (*pb.GetDataSourceRsp, error) {
	item, err := s.metadata.GetDataSource(ctx, req.GetSpaceId(), req.GetDataSourceId())
	if err != nil {
		return &pb.GetDataSourceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.GetDataSourceRsp{RetInfo: quantstore.Success("success"), DataSource: item}, nil
}

func (s *Service) ListDataSources(ctx context.Context, req *pb.ListDataSourcesReq) (*pb.ListDataSourcesRsp, error) {
	items, page, err := s.metadata.ListDataSources(ctx, req.GetSpaceId(), req.GetKind(), req.GetMarket(), req.GetPage())
	if err != nil {
		return &pb.ListDataSourcesRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDataSourcesRsp{RetInfo: quantstore.Success("success"), DataSources: items, PageResult: page}, nil
}

func (s *Service) UpsertSubject(ctx context.Context, req *pb.UpsertSubjectReq) (*pb.UpsertSubjectRsp, error) {
	item := req.GetSubject()
	if item == nil || item.GetSpaceId() == "" || (item.GetSubjectId() == "" && item.GetName() == "") {
		return &pb.UpsertSubjectRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and subject_id or name are required"))}, nil
	}
	if item.SubjectId == "" {
		item.SubjectId = defaultID(item.GetName(), "subject")
	}
	if item.SubjectType == "" {
		item.SubjectType = "custom"
	}
	created, err := s.metadata.UpsertSubject(ctx, item)
	if err != nil {
		return &pb.UpsertSubjectRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpsertSubjectRsp{RetInfo: quantstore.Success("success"), Subject: created}, nil
}

func (s *Service) GetSubject(ctx context.Context, req *pb.GetSubjectReq) (*pb.GetSubjectRsp, error) {
	item, err := s.metadata.GetSubject(ctx, req.GetSpaceId(), req.GetSubjectId())
	if err != nil {
		return &pb.GetSubjectRsp{RetInfo: quantstore.Error(pb.ErrorCode_SUBJECT_NOT_FOUND, err)}, nil
	}
	return &pb.GetSubjectRsp{RetInfo: quantstore.Success("success"), Subject: item}, nil
}

func (s *Service) ListSubjects(ctx context.Context, req *pb.ListSubjectsReq) (*pb.ListSubjectsRsp, error) {
	items, page, err := s.metadata.ListSubjects(ctx, req.GetSpaceId(), req.GetSubjectType(), req.GetMarket(), req.GetSubjectIds(), req.GetPage())
	if err != nil {
		return &pb.ListSubjectsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListSubjectsRsp{RetInfo: quantstore.Success("success"), Subjects: items, PageResult: page}, nil
}

func (s *Service) UpsertSubjectSymbol(ctx context.Context, req *pb.UpsertSubjectSymbolReq) (*pb.UpsertSubjectSymbolRsp, error) {
	item := req.GetSubjectSymbol()
	if item == nil || item.GetSpaceId() == "" || item.GetSubjectId() == "" || item.GetDataSourceId() == "" || item.GetExternalSymbol() == "" {
		return &pb.UpsertSubjectSymbolRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, subject_id, data_source_id and external_symbol are required"))}, nil
	}
	created, err := s.metadata.UpsertSubjectSymbol(ctx, item)
	if err != nil {
		return &pb.UpsertSubjectSymbolRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpsertSubjectSymbolRsp{RetInfo: quantstore.Success("success"), SubjectSymbol: created}, nil
}

func (s *Service) ListSubjectSymbols(ctx context.Context, req *pb.ListSubjectSymbolsReq) (*pb.ListSubjectSymbolsRsp, error) {
	items, page, err := s.metadata.ListSubjectSymbols(ctx, req.GetSpaceId(), req.GetSubjectId(), req.GetDataSourceId(), req.GetExternalSymbol(), req.GetPage())
	if err != nil {
		return &pb.ListSubjectSymbolsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListSubjectSymbolsRsp{RetInfo: quantstore.Success("success"), SubjectSymbols: items, PageResult: page}, nil
}

func (s *Service) CreateDataSet(ctx context.Context, req *pb.CreateDataSetReq) (*pb.CreateDataSetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetSpaceId() == "" || item.GetDataSourceId() == "" || (item.GetDatasetId() == "" && item.GetName() == "") {
		return &pb.CreateDataSetRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, data_source_id and dataset_id or name are required"))}, nil
	}
	if item.DatasetId == "" {
		item.DatasetId = defaultID(item.GetName(), "dataset")
	}
	if item.Name == "" {
		item.Name = item.GetDatasetId()
	}
	created, err := s.metadata.UpsertDataSet(ctx, item)
	if err != nil {
		return &pb.CreateDataSetRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateDataSetRsp{RetInfo: quantstore.Success("success"), Dataset: created}, nil
}

func (s *Service) UpdateDataSet(ctx context.Context, req *pb.UpdateDataSetReq) (*pb.UpdateDataSetRsp, error) {
	updated, err := s.metadata.UpsertDataSet(ctx, req.GetDataset())
	if err != nil {
		return &pb.UpdateDataSetRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateDataSetRsp{RetInfo: quantstore.Success("success"), Dataset: updated}, nil
}

func (s *Service) GetDataSet(ctx context.Context, req *pb.GetDataSetReq) (*pb.GetDataSetRsp, error) {
	item, err := s.metadata.GetDataSet(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.GetDataSetRsp{RetInfo: quantstore.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
	}
	return &pb.GetDataSetRsp{RetInfo: quantstore.Success("success"), Dataset: item}, nil
}

func (s *Service) ListDataSets(ctx context.Context, req *pb.ListDataSetsReq) (*pb.ListDataSetsRsp, error) {
	items, page, err := s.metadata.ListDataSets(ctx, req.GetSpaceId(), req.GetDataSourceId(), req.GetDataKind(), req.GetFreq(), req.GetPage())
	if err != nil {
		return &pb.ListDataSetsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDataSetsRsp{RetInfo: quantstore.Success("success"), Datasets: items, PageResult: page}, nil
}

func (s *Service) BindDataSetSubject(ctx context.Context, req *pb.BindDataSetSubjectReq) (*pb.BindDataSetSubjectRsp, error) {
	item := req.GetDatasetSubject()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetSubjectId() == "" {
		return &pb.BindDataSetSubjectRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and subject_id are required"))}, nil
	}
	created, err := s.metadata.BindDataSetSubject(ctx, item)
	if err != nil {
		return &pb.BindDataSetSubjectRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.BindDataSetSubjectRsp{RetInfo: quantstore.Success("success"), DatasetSubject: created}, nil
}

func (s *Service) ListDataSetSubjects(ctx context.Context, req *pb.ListDataSetSubjectsReq) (*pb.ListDataSetSubjectsRsp, error) {
	items, page, err := s.metadata.ListDataSetSubjectsPage(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSubjectId(), req.GetPage())
	if err != nil {
		return &pb.ListDataSetSubjectsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDataSetSubjectsRsp{RetInfo: quantstore.Success("success"), DatasetSubjects: items, PageResult: page}, nil
}

func (s *Service) CreateField(ctx context.Context, req *pb.CreateFieldReq) (*pb.CreateFieldRsp, error) {
	created, err := s.metadata.UpsertField(ctx, req.GetField())
	if err != nil {
		return &pb.CreateFieldRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateFieldRsp{RetInfo: quantstore.Success("success"), Field: created}, nil
}

func (s *Service) UpdateField(ctx context.Context, req *pb.UpdateFieldReq) (*pb.UpdateFieldRsp, error) {
	updated, err := s.metadata.UpsertField(ctx, req.GetField())
	if err != nil {
		return &pb.UpdateFieldRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateFieldRsp{RetInfo: quantstore.Success("success"), Field: updated}, nil
}

func (s *Service) GetField(ctx context.Context, req *pb.GetFieldReq) (*pb.GetFieldRsp, error) {
	item, err := s.metadata.GetField(ctx, req.GetSpaceId(), req.GetFieldId())
	if err != nil {
		return &pb.GetFieldRsp{RetInfo: quantstore.Error(pb.ErrorCode_FIELD_NOT_FOUND, err)}, nil
	}
	return &pb.GetFieldRsp{RetInfo: quantstore.Success("success"), Field: item}, nil
}

func (s *Service) ListFields(ctx context.Context, req *pb.ListFieldsReq) (*pb.ListFieldsRsp, error) {
	items, page, err := s.metadata.ListFields(ctx, req.GetSpaceId(), req.GetValueType(), req.GetPage())
	if err != nil {
		return &pb.ListFieldsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListFieldsRsp{RetInfo: quantstore.Success("success"), Fields: items, PageResult: page}, nil
}

func (s *Service) CreateFactor(ctx context.Context, req *pb.CreateFactorReq) (*pb.CreateFactorRsp, error) {
	created, err := s.metadata.UpsertFactor(ctx, req.GetFactor())
	if err != nil {
		return &pb.CreateFactorRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateFactorRsp{RetInfo: quantstore.Success("success"), Factor: created}, nil
}

func (s *Service) UpdateFactor(ctx context.Context, req *pb.UpdateFactorReq) (*pb.UpdateFactorRsp, error) {
	updated, err := s.metadata.UpsertFactor(ctx, req.GetFactor())
	if err != nil {
		return &pb.UpdateFactorRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateFactorRsp{RetInfo: quantstore.Success("success"), Factor: updated}, nil
}

func (s *Service) GetFactor(ctx context.Context, req *pb.GetFactorReq) (*pb.GetFactorRsp, error) {
	item, err := s.metadata.GetFactor(ctx, req.GetSpaceId(), req.GetFactorId())
	if err != nil {
		return &pb.GetFactorRsp{RetInfo: quantstore.Error(pb.ErrorCode_FACTOR_NOT_FOUND, err)}, nil
	}
	return &pb.GetFactorRsp{RetInfo: quantstore.Success("success"), Factor: item}, nil
}

func (s *Service) ListFactors(ctx context.Context, req *pb.ListFactorsReq) (*pb.ListFactorsRsp, error) {
	items, page, err := s.metadata.ListFactors(ctx, req.GetSpaceId(), req.GetAlgorithm(), req.GetPage())
	if err != nil {
		return &pb.ListFactorsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListFactorsRsp{RetInfo: quantstore.Success("success"), Factors: items, PageResult: page}, nil
}

func (s *Service) UpsertDataSetColumn(ctx context.Context, req *pb.UpsertDataSetColumnReq) (*pb.UpsertDataSetColumnRsp, error) {
	item := req.GetColumn()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return &pb.UpsertDataSetColumnRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and column_name are required"))}, nil
	}
	created, err := s.metadata.UpsertDataSetColumn(ctx, item)
	if err != nil {
		return &pb.UpsertDataSetColumnRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpsertDataSetColumnRsp{RetInfo: quantstore.Success("success"), Column: created}, nil
}

func (s *Service) ListDataSetColumns(ctx context.Context, req *pb.ListDataSetColumnsReq) (*pb.ListDataSetColumnsRsp, error) {
	items, page, err := s.metadata.ListDataSetColumns(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetTextIndexedOnly(), req.GetPage())
	if err != nil {
		return &pb.ListDataSetColumnsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDataSetColumnsRsp{RetInfo: quantstore.Success("success"), Columns: items, PageResult: page}, nil
}

func (s *Service) CreateStorageNode(ctx context.Context, req *pb.CreateStorageNodeReq) (*pb.CreateStorageNodeRsp, error) {
	item := req.GetNode()
	if item == nil || (item.GetNodeId() == "" && item.GetName() == "") {
		return &pb.CreateStorageNodeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id or name is required"))}, nil
	}
	if item.NodeId == "" {
		item.NodeId = defaultID(item.GetName(), "node")
	}
	if item.Name == "" {
		item.Name = item.GetNodeId()
	}
	created, err := s.metadata.UpsertStorageNode(ctx, item)
	if err != nil {
		return &pb.CreateStorageNodeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateStorageNodeRsp{RetInfo: quantstore.Success("success"), Node: created}, nil
}

func (s *Service) UpdateStorageNode(ctx context.Context, req *pb.UpdateStorageNodeReq) (*pb.UpdateStorageNodeRsp, error) {
	updated, err := s.metadata.UpsertStorageNode(ctx, req.GetNode())
	if err != nil {
		return &pb.UpdateStorageNodeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateStorageNodeRsp{RetInfo: quantstore.Success("success"), Node: updated}, nil
}

func (s *Service) GetStorageNode(ctx context.Context, req *pb.GetStorageNodeReq) (*pb.GetStorageNodeRsp, error) {
	item, err := s.metadata.GetStorageNode(ctx, req.GetNodeId())
	if err != nil {
		return &pb.GetStorageNodeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.GetStorageNodeRsp{RetInfo: quantstore.Success("success"), Node: item}, nil
}

func (s *Service) ListStorageNodes(ctx context.Context, req *pb.ListStorageNodesReq) (*pb.ListStorageNodesRsp, error) {
	items, page, err := s.metadata.ListStorageNodes(ctx, req.GetPage())
	if err != nil {
		return &pb.ListStorageNodesRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListStorageNodesRsp{RetInfo: quantstore.Success("success"), Nodes: items, PageResult: page}, nil
}

func (s *Service) CreateDevice(ctx context.Context, req *pb.CreateDeviceReq) (*pb.CreateDeviceRsp, error) {
	item := req.GetDevice()
	if item == nil || (item.GetDeviceId() == "" && item.GetName() == "") {
		return &pb.CreateDeviceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("device_id or name is required"))}, nil
	}
	if item.DeviceId == "" {
		item.DeviceId = defaultID(item.GetName(), "device")
	}
	if item.Name == "" {
		item.Name = item.GetDeviceId()
	}
	created, err := s.metadata.UpsertDevice(ctx, item)
	if err != nil {
		return &pb.CreateDeviceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateDeviceRsp{RetInfo: quantstore.Success("success"), Device: created}, nil
}

func (s *Service) UpdateDevice(ctx context.Context, req *pb.UpdateDeviceReq) (*pb.UpdateDeviceRsp, error) {
	updated, err := s.metadata.UpsertDevice(ctx, req.GetDevice())
	if err != nil {
		return &pb.UpdateDeviceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateDeviceRsp{RetInfo: quantstore.Success("success"), Device: updated}, nil
}

func (s *Service) GetDevice(ctx context.Context, req *pb.GetDeviceReq) (*pb.GetDeviceRsp, error) {
	item, err := s.metadata.GetDevice(ctx, req.GetDeviceId())
	if err != nil {
		return &pb.GetDeviceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.GetDeviceRsp{RetInfo: quantstore.Success("success"), Device: item}, nil
}

func (s *Service) ListDevices(ctx context.Context, req *pb.ListDevicesReq) (*pb.ListDevicesRsp, error) {
	items, page, err := s.metadata.ListDevices(ctx, req.GetNodeId(), req.GetEngine(), req.GetPage())
	if err != nil {
		return &pb.ListDevicesRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListDevicesRsp{RetInfo: quantstore.Success("success"), Devices: items, PageResult: page}, nil
}

func (s *Service) CreateStorageRoute(ctx context.Context, req *pb.CreateStorageRouteReq) (*pb.CreateStorageRouteRsp, error) {
	item := req.GetStorageRoute()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetNodeId() == "" {
		return &pb.CreateStorageRouteRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and node_id are required"))}, nil
	}
	if item.RouteId == "" {
		item.RouteId = defaultID(strings.Join([]string{item.GetSpaceId(), item.GetDatasetId(), item.GetSubjectId(), item.GetNodeId()}, "-"), "route")
	}
	created, err := s.metadata.UpsertStorageRoute(ctx, item)
	if err != nil {
		return &pb.CreateStorageRouteRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.CreateStorageRouteRsp{RetInfo: quantstore.Success("success"), StorageRoute: created}, nil
}

func (s *Service) UpdateStorageRoute(ctx context.Context, req *pb.UpdateStorageRouteReq) (*pb.UpdateStorageRouteRsp, error) {
	updated, err := s.metadata.UpsertStorageRoute(ctx, req.GetStorageRoute())
	if err != nil {
		return &pb.UpdateStorageRouteRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.UpdateStorageRouteRsp{RetInfo: quantstore.Success("success"), StorageRoute: updated}, nil
}

func (s *Service) GetStorageRoute(ctx context.Context, req *pb.GetStorageRouteReq) (*pb.GetStorageRouteRsp, error) {
	item, err := s.metadata.GetStorageRoute(ctx, req.GetSpaceId(), req.GetRouteId())
	if err != nil {
		return &pb.GetStorageRouteRsp{RetInfo: quantstore.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	return &pb.GetStorageRouteRsp{RetInfo: quantstore.Success("success"), StorageRoute: item}, nil
}

func (s *Service) ListStorageRoutes(ctx context.Context, req *pb.ListStorageRoutesReq) (*pb.ListStorageRoutesRsp, error) {
	items, page, err := s.metadata.ListStorageRoutes(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSubjectId(), req.GetNodeId(), req.GetPage())
	if err != nil {
		return &pb.ListStorageRoutesRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ListStorageRoutesRsp{RetInfo: quantstore.Success("success"), StorageRoutes: items, PageResult: page}, nil
}

func (s *Service) RegisterArchiveFile(ctx context.Context, req *pb.RegisterArchiveFileReq) (*pb.RegisterArchiveFileRsp, error) {
	item := req.GetArchiveFile()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetDeviceId() == "" || item.GetFileUri() == "" {
		return &pb.RegisterArchiveFileRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id, device_id and file_uri are required"))}, nil
	}
	if item.ArchiveFileId == "" {
		item.ArchiveFileId = defaultID(strings.Join([]string{item.GetSpaceId(), item.GetDatasetId(), item.GetPartitionKey(), item.GetFileUri()}, "-"), "archive_file")
	}
	created, err := s.metadata.RegisterArchiveFile(ctx, item)
	if err != nil {
		return &pb.RegisterArchiveFileRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.RegisterArchiveFileRsp{RetInfo: quantstore.Success("success"), ArchiveFile: created}, nil
}

func (s *Service) ListArchiveFiles(ctx context.Context, req *pb.ListArchiveFilesReq) (*pb.ListArchiveFilesRsp, error) {
	items, _, err := s.metadata.ListArchiveFiles(ctx, req.GetSpaceId(), req.GetDatasetId(), nil)
	if err != nil {
		return &pb.ListArchiveFilesRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
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
	return &pb.ListArchiveFilesRsp{RetInfo: quantstore.Success("success"), ArchiveFiles: paged, PageResult: page}, nil
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
