package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
)

type Service struct {
	store *quantstore.Store

	mu              sync.RWMutex
	spaces          map[string]*pb.Space
	views           map[string]*pb.View
	viewColumns     map[string]*pb.ViewColumn
	dataSources     map[string]*pb.DataSource
	subjects        map[string]*pb.Subject
	subjectSymbols  map[string]*pb.SubjectSymbol
	datasets        map[string]*pb.DataSet
	datasetSubjects map[string]*pb.DataSetSubject
	fields          map[string]*pb.Field
	factors         map[string]*pb.Factor
	datasetColumns  map[string]*pb.DataSetColumn
	storageNodes    map[string]*pb.StorageNode
	devices         map[string]*pb.Device
	storageRoutes   map[string]*pb.StorageRoute
	archiveFiles    map[string]*pb.ArchiveFile
}

var (
	_ pb.MetadataServiceService = (*Service)(nil)
	_ pb.DataServiceService     = (*Service)(nil)
	_ pb.QueryServiceService    = (*Service)(nil)
	_ pb.AdapterServiceService  = (*Service)(nil)
)

func NewService(root string) *Service {
	return &Service{
		store:           quantstore.New(root),
		spaces:          make(map[string]*pb.Space),
		views:           make(map[string]*pb.View),
		viewColumns:     make(map[string]*pb.ViewColumn),
		dataSources:     make(map[string]*pb.DataSource),
		subjects:        make(map[string]*pb.Subject),
		subjectSymbols:  make(map[string]*pb.SubjectSymbol),
		datasets:        make(map[string]*pb.DataSet),
		datasetSubjects: make(map[string]*pb.DataSetSubject),
		fields:          make(map[string]*pb.Field),
		factors:         make(map[string]*pb.Factor),
		datasetColumns:  make(map[string]*pb.DataSetColumn),
		storageNodes:    make(map[string]*pb.StorageNode),
		devices:         make(map[string]*pb.Device),
		storageRoutes:   make(map[string]*pb.StorageRoute),
		archiveFiles:    make(map[string]*pb.ArchiveFile),
	}
}

func (s *Service) CreateSpace(_ context.Context, req *pb.CreateSpaceReq) (*pb.CreateSpaceRsp, error) {
	space := req.GetSpace()
	if space == nil || (space.GetSpaceId() == "" && space.GetName() == "") {
		return &pb.CreateSpaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id or name is required"))}, nil
	}
	if space.SpaceId == "" {
		space.SpaceId = defaultID(space.GetName(), "space")
	}
	s.mu.Lock()
	s.spaces[space.GetSpaceId()] = space
	s.mu.Unlock()
	return &pb.CreateSpaceRsp{RetInfo: quantstore.Success("success"), Space: space}, nil
}

func (s *Service) UpdateSpace(_ context.Context, req *pb.UpdateSpaceReq) (*pb.UpdateSpaceRsp, error) {
	space := req.GetSpace()
	if space == nil || space.GetSpaceId() == "" {
		return &pb.UpdateSpaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id is required"))}, nil
	}
	s.mu.Lock()
	s.spaces[space.GetSpaceId()] = space
	s.mu.Unlock()
	return &pb.UpdateSpaceRsp{RetInfo: quantstore.Success("success"), Space: space}, nil
}

func (s *Service) GetSpace(_ context.Context, req *pb.GetSpaceReq) (*pb.GetSpaceRsp, error) {
	s.mu.RLock()
	space := s.spaces[req.GetSpaceId()]
	s.mu.RUnlock()
	if space == nil {
		return &pb.GetSpaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_SPACE_NOT_FOUND, fmt.Errorf("space %s not found", req.GetSpaceId()))}, nil
	}
	return &pb.GetSpaceRsp{RetInfo: quantstore.Success("success"), Space: space}, nil
}

func (s *Service) ListSpaces(_ context.Context, req *pb.ListSpacesReq) (*pb.ListSpacesRsp, error) {
	s.mu.RLock()
	items := make([]*pb.Space, 0, len(s.spaces))
	for _, item := range s.spaces {
		if req.GetOwner() != "" && item.GetOwner() != req.GetOwner() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetSpaceId() < items[j].GetSpaceId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListSpacesRsp{RetInfo: quantstore.Success("success"), Spaces: paged, PageResult: page}, nil
}

func (s *Service) CreateView(_ context.Context, req *pb.CreateViewReq) (*pb.CreateViewRsp, error) {
	view := req.GetView()
	if view == nil || view.GetSpaceId() == "" || (view.GetViewId() == "" && view.GetName() == "") {
		return &pb.CreateViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id or name are required"))}, nil
	}
	if view.ViewId == "" {
		view.ViewId = defaultID(view.GetName(), "view")
	}
	s.mu.Lock()
	s.views[metadataKey(view.GetSpaceId(), view.GetViewId())] = view
	for _, column := range view.GetColumns() {
		if column.GetSpaceId() == "" {
			column.SpaceId = view.GetSpaceId()
		}
		if column.GetViewId() == "" {
			column.ViewId = view.GetViewId()
		}
		if column.GetColumnName() != "" {
			s.viewColumns[metadataKey(column.GetSpaceId(), column.GetViewId(), column.GetColumnName())] = column
		}
	}
	s.mu.Unlock()
	return &pb.CreateViewRsp{RetInfo: quantstore.Success("success"), View: view}, nil
}

func (s *Service) UpdateView(_ context.Context, req *pb.UpdateViewReq) (*pb.UpdateViewRsp, error) {
	view := req.GetView()
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
		return &pb.UpdateViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and view_id are required"))}, nil
	}
	s.mu.Lock()
	s.views[metadataKey(view.GetSpaceId(), view.GetViewId())] = view
	s.mu.Unlock()
	return &pb.UpdateViewRsp{RetInfo: quantstore.Success("success"), View: view}, nil
}

func (s *Service) GetView(_ context.Context, req *pb.GetViewReq) (*pb.GetViewRsp, error) {
	s.mu.RLock()
	view := s.views[metadataKey(req.GetSpaceId(), req.GetViewId())]
	s.mu.RUnlock()
	if view == nil {
		return &pb.GetViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_VIEW_NOT_FOUND, fmt.Errorf("view %s not found", req.GetViewId()))}, nil
	}
	return &pb.GetViewRsp{RetInfo: quantstore.Success("success"), View: view}, nil
}

func (s *Service) ListViews(_ context.Context, req *pb.ListViewsReq) (*pb.ListViewsRsp, error) {
	s.mu.RLock()
	var items []*pb.View
	for _, item := range s.views {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetDatasetId() != "" && !containsString(item.GetDatasetIds(), req.GetDatasetId()) {
			continue
		}
		if req.GetStatus() != "" && item.GetStatus() != req.GetStatus() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetViewId() < items[j].GetViewId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListViewsRsp{RetInfo: quantstore.Success("success"), Views: paged, PageResult: page}, nil
}

func (s *Service) UpsertViewColumn(_ context.Context, req *pb.UpsertViewColumnReq) (*pb.UpsertViewColumnRsp, error) {
	column := req.GetColumn()
	if column == nil || column.GetSpaceId() == "" || column.GetViewId() == "" || column.GetColumnName() == "" {
		return &pb.UpsertViewColumnRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, view_id and column_name are required"))}, nil
	}
	s.mu.Lock()
	s.viewColumns[metadataKey(column.GetSpaceId(), column.GetViewId(), column.GetColumnName())] = column
	if view := s.views[metadataKey(column.GetSpaceId(), column.GetViewId())]; view != nil {
		view.Columns = upsertViewColumn(view.GetColumns(), column)
	}
	s.mu.Unlock()
	return &pb.UpsertViewColumnRsp{RetInfo: quantstore.Success("success"), Column: column}, nil
}

func (s *Service) ListViewColumns(_ context.Context, req *pb.ListViewColumnsReq) (*pb.ListViewColumnsRsp, error) {
	s.mu.RLock()
	var items []*pb.ViewColumn
	for _, item := range s.viewColumns {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetViewId() != "" && item.GetViewId() != req.GetViewId() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].GetSortOrder() == items[j].GetSortOrder() {
			return items[i].GetColumnName() < items[j].GetColumnName()
		}
		return items[i].GetSortOrder() < items[j].GetSortOrder()
	})
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListViewColumnsRsp{RetInfo: quantstore.Success("success"), Columns: paged, PageResult: page}, nil
}

func (s *Service) CreateDataSource(_ context.Context, req *pb.CreateDataSourceReq) (*pb.CreateDataSourceRsp, error) {
	item := req.GetDataSource()
	if item == nil || item.GetSpaceId() == "" || (item.GetDataSourceId() == "" && item.GetName() == "") {
		return &pb.CreateDataSourceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and data_source_id or name are required"))}, nil
	}
	if item.DataSourceId == "" {
		item.DataSourceId = defaultID(item.GetName(), "data_source")
	}
	s.mu.Lock()
	s.dataSources[metadataKey(item.GetSpaceId(), item.GetDataSourceId())] = item
	s.mu.Unlock()
	return &pb.CreateDataSourceRsp{RetInfo: quantstore.Success("success"), DataSource: item}, nil
}

func (s *Service) UpdateDataSource(_ context.Context, req *pb.UpdateDataSourceReq) (*pb.UpdateDataSourceRsp, error) {
	item := req.GetDataSource()
	if item == nil || item.GetSpaceId() == "" || item.GetDataSourceId() == "" {
		return &pb.UpdateDataSourceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and data_source_id are required"))}, nil
	}
	s.mu.Lock()
	s.dataSources[metadataKey(item.GetSpaceId(), item.GetDataSourceId())] = item
	s.mu.Unlock()
	return &pb.UpdateDataSourceRsp{RetInfo: quantstore.Success("success"), DataSource: item}, nil
}

func (s *Service) GetDataSource(_ context.Context, req *pb.GetDataSourceReq) (*pb.GetDataSourceRsp, error) {
	s.mu.RLock()
	item := s.dataSources[metadataKey(req.GetSpaceId(), req.GetDataSourceId())]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetDataSourceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("data_source not found"))}, nil
	}
	return &pb.GetDataSourceRsp{RetInfo: quantstore.Success("success"), DataSource: item}, nil
}

func (s *Service) ListDataSources(_ context.Context, req *pb.ListDataSourcesReq) (*pb.ListDataSourcesRsp, error) {
	s.mu.RLock()
	var items []*pb.DataSource
	for _, item := range s.dataSources {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetKind() != "" && item.GetKind() != req.GetKind() {
			continue
		}
		if req.GetMarket() != "" && item.GetMarket() != req.GetMarket() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetDataSourceId() < items[j].GetDataSourceId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListDataSourcesRsp{RetInfo: quantstore.Success("success"), DataSources: paged, PageResult: page}, nil
}

func (s *Service) UpsertSubject(_ context.Context, req *pb.UpsertSubjectReq) (*pb.UpsertSubjectRsp, error) {
	item := req.GetSubject()
	if item == nil || item.GetSpaceId() == "" || (item.GetSubjectId() == "" && item.GetName() == "") {
		return &pb.UpsertSubjectRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and subject_id or name are required"))}, nil
	}
	if item.SubjectId == "" {
		item.SubjectId = defaultID(item.GetName(), "subject")
	}
	s.mu.Lock()
	s.subjects[metadataKey(item.GetSpaceId(), item.GetSubjectId())] = item
	s.mu.Unlock()
	return &pb.UpsertSubjectRsp{RetInfo: quantstore.Success("success"), Subject: item}, nil
}

func (s *Service) GetSubject(_ context.Context, req *pb.GetSubjectReq) (*pb.GetSubjectRsp, error) {
	s.mu.RLock()
	item := s.subjects[metadataKey(req.GetSpaceId(), req.GetSubjectId())]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetSubjectRsp{RetInfo: quantstore.Error(pb.ErrorCode_SUBJECT_NOT_FOUND, fmt.Errorf("subject %s not found", req.GetSubjectId()))}, nil
	}
	return &pb.GetSubjectRsp{RetInfo: quantstore.Success("success"), Subject: item}, nil
}

func (s *Service) ListSubjects(_ context.Context, req *pb.ListSubjectsReq) (*pb.ListSubjectsRsp, error) {
	allow := stringSet(req.GetSubjectIds())
	s.mu.RLock()
	var items []*pb.Subject
	for _, item := range s.subjects {
		if len(allow) > 0 && !allow[item.GetSubjectId()] {
			continue
		}
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetSubjectType() != "" && item.GetSubjectType() != req.GetSubjectType() {
			continue
		}
		if req.GetMarket() != "" && item.GetMarket() != req.GetMarket() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetSubjectId() < items[j].GetSubjectId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListSubjectsRsp{RetInfo: quantstore.Success("success"), Subjects: paged, PageResult: page}, nil
}

func (s *Service) UpsertSubjectSymbol(_ context.Context, req *pb.UpsertSubjectSymbolReq) (*pb.UpsertSubjectSymbolRsp, error) {
	item := req.GetSubjectSymbol()
	if item == nil || item.GetSpaceId() == "" || item.GetSubjectId() == "" || item.GetDataSourceId() == "" || item.GetExternalSymbol() == "" {
		return &pb.UpsertSubjectSymbolRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, subject_id, data_source_id and external_symbol are required"))}, nil
	}
	s.mu.Lock()
	s.subjectSymbols[metadataKey(item.GetSpaceId(), item.GetDataSourceId(), item.GetExternalSymbol())] = item
	s.mu.Unlock()
	return &pb.UpsertSubjectSymbolRsp{RetInfo: quantstore.Success("success"), SubjectSymbol: item}, nil
}

func (s *Service) ListSubjectSymbols(_ context.Context, req *pb.ListSubjectSymbolsReq) (*pb.ListSubjectSymbolsRsp, error) {
	s.mu.RLock()
	var items []*pb.SubjectSymbol
	for _, item := range s.subjectSymbols {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetSubjectId() != "" && item.GetSubjectId() != req.GetSubjectId() {
			continue
		}
		if req.GetDataSourceId() != "" && item.GetDataSourceId() != req.GetDataSourceId() {
			continue
		}
		if req.GetExternalSymbol() != "" && item.GetExternalSymbol() != req.GetExternalSymbol() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		return metadataKey(items[i].GetSpaceId(), items[i].GetDataSourceId(), items[i].GetExternalSymbol()) < metadataKey(items[j].GetSpaceId(), items[j].GetDataSourceId(), items[j].GetExternalSymbol())
	})
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListSubjectSymbolsRsp{RetInfo: quantstore.Success("success"), SubjectSymbols: paged, PageResult: page}, nil
}

func (s *Service) CreateDataSet(_ context.Context, req *pb.CreateDataSetReq) (*pb.CreateDataSetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetSpaceId() == "" || item.GetDataSourceId() == "" || (item.GetDatasetId() == "" && item.GetName() == "") {
		return &pb.CreateDataSetRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, data_source_id and dataset_id or name are required"))}, nil
	}
	if item.DatasetId == "" {
		item.DatasetId = defaultID(item.GetName(), "dataset")
	}
	s.mu.Lock()
	s.datasets[metadataKey(item.GetSpaceId(), item.GetDatasetId())] = item
	s.mu.Unlock()
	return &pb.CreateDataSetRsp{RetInfo: quantstore.Success("success"), Dataset: item}, nil
}

func (s *Service) UpdateDataSet(_ context.Context, req *pb.UpdateDataSetReq) (*pb.UpdateDataSetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" {
		return &pb.UpdateDataSetRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	s.mu.Lock()
	s.datasets[metadataKey(item.GetSpaceId(), item.GetDatasetId())] = item
	s.mu.Unlock()
	return &pb.UpdateDataSetRsp{RetInfo: quantstore.Success("success"), Dataset: item}, nil
}

func (s *Service) GetDataSet(_ context.Context, req *pb.GetDataSetReq) (*pb.GetDataSetRsp, error) {
	s.mu.RLock()
	item := s.datasets[metadataKey(req.GetSpaceId(), req.GetDatasetId())]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetDataSetRsp{RetInfo: quantstore.Error(pb.ErrorCode_DATASET_NOT_FOUND, fmt.Errorf("dataset %s not found", req.GetDatasetId()))}, nil
	}
	return &pb.GetDataSetRsp{RetInfo: quantstore.Success("success"), Dataset: item}, nil
}

func (s *Service) ListDataSets(_ context.Context, req *pb.ListDataSetsReq) (*pb.ListDataSetsRsp, error) {
	s.mu.RLock()
	var items []*pb.DataSet
	for _, item := range s.datasets {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetDataSourceId() != "" && item.GetDataSourceId() != req.GetDataSourceId() {
			continue
		}
		if req.GetDataKind() != pb.DataKind_DATA_KIND_UNSPECIFIED && item.GetDataKind() != req.GetDataKind() {
			continue
		}
		if req.GetFreq() != "" && !containsString(item.GetFreqs(), req.GetFreq()) {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetDatasetId() < items[j].GetDatasetId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListDataSetsRsp{RetInfo: quantstore.Success("success"), Datasets: paged, PageResult: page}, nil
}

func (s *Service) BindDataSetSubject(_ context.Context, req *pb.BindDataSetSubjectReq) (*pb.BindDataSetSubjectRsp, error) {
	item := req.GetDatasetSubject()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetSubjectId() == "" {
		return &pb.BindDataSetSubjectRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and subject_id are required"))}, nil
	}
	s.mu.Lock()
	s.datasetSubjects[metadataKey(item.GetSpaceId(), item.GetDatasetId(), item.GetSubjectId())] = item
	s.mu.Unlock()
	return &pb.BindDataSetSubjectRsp{RetInfo: quantstore.Success("success"), DatasetSubject: item}, nil
}

func (s *Service) ListDataSetSubjects(_ context.Context, req *pb.ListDataSetSubjectsReq) (*pb.ListDataSetSubjectsRsp, error) {
	s.mu.RLock()
	var items []*pb.DataSetSubject
	for _, item := range s.datasetSubjects {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetDatasetId() != "" && item.GetDatasetId() != req.GetDatasetId() {
			continue
		}
		if req.GetSubjectId() != "" && item.GetSubjectId() != req.GetSubjectId() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		return metadataKey(items[i].GetSpaceId(), items[i].GetDatasetId(), items[i].GetSubjectId()) < metadataKey(items[j].GetSpaceId(), items[j].GetDatasetId(), items[j].GetSubjectId())
	})
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListDataSetSubjectsRsp{RetInfo: quantstore.Success("success"), DatasetSubjects: paged, PageResult: page}, nil
}

func (s *Service) CreateField(_ context.Context, req *pb.CreateFieldReq) (*pb.CreateFieldRsp, error) {
	item := req.GetField()
	if item == nil || item.GetSpaceId() == "" || item.GetFieldId() == "" {
		return &pb.CreateFieldRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and field_id are required"))}, nil
	}
	s.mu.Lock()
	s.fields[metadataKey(item.GetSpaceId(), item.GetFieldId())] = item
	s.mu.Unlock()
	return &pb.CreateFieldRsp{RetInfo: quantstore.Success("success"), Field: item}, nil
}

func (s *Service) UpdateField(_ context.Context, req *pb.UpdateFieldReq) (*pb.UpdateFieldRsp, error) {
	item := req.GetField()
	if item == nil || item.GetSpaceId() == "" || item.GetFieldId() == "" {
		return &pb.UpdateFieldRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and field_id are required"))}, nil
	}
	s.mu.Lock()
	s.fields[metadataKey(item.GetSpaceId(), item.GetFieldId())] = item
	s.mu.Unlock()
	return &pb.UpdateFieldRsp{RetInfo: quantstore.Success("success"), Field: item}, nil
}

func (s *Service) GetField(_ context.Context, req *pb.GetFieldReq) (*pb.GetFieldRsp, error) {
	s.mu.RLock()
	item := s.fields[metadataKey(req.GetSpaceId(), req.GetFieldId())]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetFieldRsp{RetInfo: quantstore.Error(pb.ErrorCode_FIELD_NOT_FOUND, errors.New("field not found"))}, nil
	}
	return &pb.GetFieldRsp{RetInfo: quantstore.Success("success"), Field: item}, nil
}

func (s *Service) ListFields(_ context.Context, req *pb.ListFieldsReq) (*pb.ListFieldsRsp, error) {
	s.mu.RLock()
	var items []*pb.Field
	for _, item := range s.fields {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetValueType() != pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED && item.GetValueType() != req.GetValueType() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetFieldId() < items[j].GetFieldId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListFieldsRsp{RetInfo: quantstore.Success("success"), Fields: paged, PageResult: page}, nil
}

func (s *Service) CreateFactor(_ context.Context, req *pb.CreateFactorReq) (*pb.CreateFactorRsp, error) {
	item := req.GetFactor()
	if item == nil || item.GetSpaceId() == "" || item.GetFactorId() == "" {
		return &pb.CreateFactorRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and factor_id are required"))}, nil
	}
	s.mu.Lock()
	s.factors[metadataKey(item.GetSpaceId(), item.GetFactorId())] = item
	s.mu.Unlock()
	return &pb.CreateFactorRsp{RetInfo: quantstore.Success("success"), Factor: item}, nil
}

func (s *Service) UpdateFactor(_ context.Context, req *pb.UpdateFactorReq) (*pb.UpdateFactorRsp, error) {
	item := req.GetFactor()
	if item == nil || item.GetSpaceId() == "" || item.GetFactorId() == "" {
		return &pb.UpdateFactorRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and factor_id are required"))}, nil
	}
	s.mu.Lock()
	s.factors[metadataKey(item.GetSpaceId(), item.GetFactorId())] = item
	s.mu.Unlock()
	return &pb.UpdateFactorRsp{RetInfo: quantstore.Success("success"), Factor: item}, nil
}

func (s *Service) GetFactor(_ context.Context, req *pb.GetFactorReq) (*pb.GetFactorRsp, error) {
	s.mu.RLock()
	item := s.factors[metadataKey(req.GetSpaceId(), req.GetFactorId())]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetFactorRsp{RetInfo: quantstore.Error(pb.ErrorCode_FACTOR_NOT_FOUND, errors.New("factor not found"))}, nil
	}
	return &pb.GetFactorRsp{RetInfo: quantstore.Success("success"), Factor: item}, nil
}

func (s *Service) ListFactors(_ context.Context, req *pb.ListFactorsReq) (*pb.ListFactorsRsp, error) {
	s.mu.RLock()
	var items []*pb.Factor
	for _, item := range s.factors {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetAlgorithm() != "" && item.GetAlgorithm() != req.GetAlgorithm() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetFactorId() < items[j].GetFactorId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListFactorsRsp{RetInfo: quantstore.Success("success"), Factors: paged, PageResult: page}, nil
}

func (s *Service) UpsertDataSetColumn(_ context.Context, req *pb.UpsertDataSetColumnReq) (*pb.UpsertDataSetColumnRsp, error) {
	item := req.GetColumn()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return &pb.UpsertDataSetColumnRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and column_name are required"))}, nil
	}
	s.mu.Lock()
	s.datasetColumns[metadataKey(item.GetSpaceId(), item.GetDatasetId(), item.GetColumnName())] = item
	s.mu.Unlock()
	return &pb.UpsertDataSetColumnRsp{RetInfo: quantstore.Success("success"), Column: item}, nil
}

func (s *Service) ListDataSetColumns(_ context.Context, req *pb.ListDataSetColumnsReq) (*pb.ListDataSetColumnsRsp, error) {
	s.mu.RLock()
	var items []*pb.DataSetColumn
	for _, item := range s.datasetColumns {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetDatasetId() != "" && item.GetDatasetId() != req.GetDatasetId() {
			continue
		}
		if req.GetTextIndexedOnly() && !item.GetTextIndexed() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetColumnName() < items[j].GetColumnName() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListDataSetColumnsRsp{RetInfo: quantstore.Success("success"), Columns: paged, PageResult: page}, nil
}

func (s *Service) CreateStorageNode(_ context.Context, req *pb.CreateStorageNodeReq) (*pb.CreateStorageNodeRsp, error) {
	item := req.GetNode()
	if item == nil || (item.GetNodeId() == "" && item.GetName() == "") {
		return &pb.CreateStorageNodeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id or name is required"))}, nil
	}
	if item.NodeId == "" {
		item.NodeId = defaultID(item.GetName(), "node")
	}
	s.mu.Lock()
	s.storageNodes[item.GetNodeId()] = item
	s.mu.Unlock()
	return &pb.CreateStorageNodeRsp{RetInfo: quantstore.Success("success"), Node: item}, nil
}

func (s *Service) UpdateStorageNode(_ context.Context, req *pb.UpdateStorageNodeReq) (*pb.UpdateStorageNodeRsp, error) {
	item := req.GetNode()
	if item == nil || item.GetNodeId() == "" {
		return &pb.UpdateStorageNodeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id is required"))}, nil
	}
	s.mu.Lock()
	s.storageNodes[item.GetNodeId()] = item
	s.mu.Unlock()
	return &pb.UpdateStorageNodeRsp{RetInfo: quantstore.Success("success"), Node: item}, nil
}

func (s *Service) GetStorageNode(_ context.Context, req *pb.GetStorageNodeReq) (*pb.GetStorageNodeRsp, error) {
	s.mu.RLock()
	item := s.storageNodes[req.GetNodeId()]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetStorageNodeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("storage_node not found"))}, nil
	}
	return &pb.GetStorageNodeRsp{RetInfo: quantstore.Success("success"), Node: item}, nil
}

func (s *Service) ListStorageNodes(_ context.Context, req *pb.ListStorageNodesReq) (*pb.ListStorageNodesRsp, error) {
	s.mu.RLock()
	items := values(s.storageNodes)
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetNodeId() < items[j].GetNodeId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListStorageNodesRsp{RetInfo: quantstore.Success("success"), Nodes: paged, PageResult: page}, nil
}

func (s *Service) CreateDevice(_ context.Context, req *pb.CreateDeviceReq) (*pb.CreateDeviceRsp, error) {
	item := req.GetDevice()
	if item == nil || (item.GetDeviceId() == "" && item.GetName() == "") {
		return &pb.CreateDeviceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("device_id or name is required"))}, nil
	}
	if item.DeviceId == "" {
		item.DeviceId = defaultID(item.GetName(), "device")
	}
	s.mu.Lock()
	s.devices[item.GetDeviceId()] = item
	s.mu.Unlock()
	return &pb.CreateDeviceRsp{RetInfo: quantstore.Success("success"), Device: item}, nil
}

func (s *Service) UpdateDevice(_ context.Context, req *pb.UpdateDeviceReq) (*pb.UpdateDeviceRsp, error) {
	item := req.GetDevice()
	if item == nil || item.GetDeviceId() == "" {
		return &pb.UpdateDeviceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("device_id is required"))}, nil
	}
	s.mu.Lock()
	s.devices[item.GetDeviceId()] = item
	s.mu.Unlock()
	return &pb.UpdateDeviceRsp{RetInfo: quantstore.Success("success"), Device: item}, nil
}

func (s *Service) GetDevice(_ context.Context, req *pb.GetDeviceReq) (*pb.GetDeviceRsp, error) {
	s.mu.RLock()
	item := s.devices[req.GetDeviceId()]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetDeviceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("device not found"))}, nil
	}
	return &pb.GetDeviceRsp{RetInfo: quantstore.Success("success"), Device: item}, nil
}

func (s *Service) ListDevices(_ context.Context, req *pb.ListDevicesReq) (*pb.ListDevicesRsp, error) {
	s.mu.RLock()
	var items []*pb.Device
	for _, item := range s.devices {
		if req.GetNodeId() != "" && item.GetNodeId() != req.GetNodeId() {
			continue
		}
		if req.GetEngine() != "" && item.GetEngine() != req.GetEngine() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetDeviceId() < items[j].GetDeviceId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListDevicesRsp{RetInfo: quantstore.Success("success"), Devices: paged, PageResult: page}, nil
}

func (s *Service) CreateStorageRoute(_ context.Context, req *pb.CreateStorageRouteReq) (*pb.CreateStorageRouteRsp, error) {
	item := req.GetStorageRoute()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetNodeId() == "" {
		return &pb.CreateStorageRouteRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and node_id are required"))}, nil
	}
	if item.RouteId == "" {
		item.RouteId = defaultID(strings.Join([]string{item.GetSpaceId(), item.GetDatasetId(), item.GetSubjectId(), item.GetNodeId()}, "-"), "route")
	}
	s.mu.Lock()
	s.storageRoutes[metadataKey(item.GetSpaceId(), item.GetRouteId())] = item
	s.mu.Unlock()
	return &pb.CreateStorageRouteRsp{RetInfo: quantstore.Success("success"), StorageRoute: item}, nil
}

func (s *Service) UpdateStorageRoute(_ context.Context, req *pb.UpdateStorageRouteReq) (*pb.UpdateStorageRouteRsp, error) {
	item := req.GetStorageRoute()
	if item == nil || item.GetSpaceId() == "" || item.GetRouteId() == "" {
		return &pb.UpdateStorageRouteRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and route_id are required"))}, nil
	}
	s.mu.Lock()
	s.storageRoutes[metadataKey(item.GetSpaceId(), item.GetRouteId())] = item
	s.mu.Unlock()
	return &pb.UpdateStorageRouteRsp{RetInfo: quantstore.Success("success"), StorageRoute: item}, nil
}

func (s *Service) GetStorageRoute(_ context.Context, req *pb.GetStorageRouteReq) (*pb.GetStorageRouteRsp, error) {
	s.mu.RLock()
	item := s.storageRoutes[metadataKey(req.GetSpaceId(), req.GetRouteId())]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetStorageRouteRsp{RetInfo: quantstore.Error(pb.ErrorCode_ROUTE_NOT_FOUND, errors.New("storage_route not found"))}, nil
	}
	return &pb.GetStorageRouteRsp{RetInfo: quantstore.Success("success"), StorageRoute: item}, nil
}

func (s *Service) ListStorageRoutes(_ context.Context, req *pb.ListStorageRoutesReq) (*pb.ListStorageRoutesRsp, error) {
	s.mu.RLock()
	var items []*pb.StorageRoute
	for _, item := range s.storageRoutes {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetDatasetId() != "" && item.GetDatasetId() != req.GetDatasetId() {
			continue
		}
		if req.GetSubjectId() != "" && item.GetSubjectId() != req.GetSubjectId() {
			continue
		}
		if req.GetNodeId() != "" && item.GetNodeId() != req.GetNodeId() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetRouteId() < items[j].GetRouteId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListStorageRoutesRsp{RetInfo: quantstore.Success("success"), StorageRoutes: paged, PageResult: page}, nil
}

func (s *Service) RegisterArchiveFile(_ context.Context, req *pb.RegisterArchiveFileReq) (*pb.RegisterArchiveFileRsp, error) {
	item := req.GetArchiveFile()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetDeviceId() == "" || item.GetFileUri() == "" {
		return &pb.RegisterArchiveFileRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id, device_id and file_uri are required"))}, nil
	}
	if item.ArchiveFileId == "" {
		item.ArchiveFileId = defaultID(strings.Join([]string{item.GetSpaceId(), item.GetDatasetId(), item.GetPartitionKey(), item.GetFileUri()}, "-"), "archive_file")
	}
	s.mu.Lock()
	s.archiveFiles[metadataKey(item.GetSpaceId(), item.GetArchiveFileId())] = item
	s.mu.Unlock()
	return &pb.RegisterArchiveFileRsp{RetInfo: quantstore.Success("success"), ArchiveFile: item}, nil
}

func (s *Service) ListArchiveFiles(_ context.Context, req *pb.ListArchiveFilesReq) (*pb.ListArchiveFilesRsp, error) {
	s.mu.RLock()
	var items []*pb.ArchiveFile
	for _, item := range s.archiveFiles {
		if req.GetSpaceId() != "" && item.GetSpaceId() != req.GetSpaceId() {
			continue
		}
		if req.GetDatasetId() != "" && item.GetDatasetId() != req.GetDatasetId() {
			continue
		}
		if req.GetDeviceId() != "" && item.GetDeviceId() != req.GetDeviceId() {
			continue
		}
		if req.GetPartitionKey() != "" && item.GetPartitionKey() != req.GetPartitionKey() {
			continue
		}
		if !archiveFileOverlaps(item, req.GetTimeRange()) {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetArchiveFileId() < items[j].GetArchiveFileId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListArchiveFilesRsp{RetInfo: quantstore.Success("success"), ArchiveFiles: paged, PageResult: page}, nil
}

func metadataKey(parts ...string) string {
	return strings.Join(parts, "|")
}

func defaultID(name, prefix string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return prefix + "_" + xid.New().String()
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_")
	return replacer.Replace(name)
}

func values[T any](m map[string]T) []T {
	items := make([]T, 0, len(m))
	for _, item := range m {
		items = append(items, item)
	}
	return items
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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stringSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func upsertViewColumn(columns []*pb.ViewColumn, column *pb.ViewColumn) []*pb.ViewColumn {
	for i, existing := range columns {
		if existing.GetColumnName() == column.GetColumnName() {
			columns[i] = column
			return columns
		}
	}
	return append(columns, column)
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
