package access

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// 本文件聚合数据源、主体、数据集、字段、因子及其列绑定相关的元数据 CRUD 入口。

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
		return &pb.CreateDataSourceRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateDataSourceRsp{RetInfo: response.Success("success"), DataSource: created}, nil
}

func (s *Service) UpdateDataSource(ctx context.Context, req *pb.UpdateDataSourceReq) (*pb.UpdateDataSourceRsp, error) {
	updated, err := s.metadata.UpsertDataSource(ctx, req.GetDataSource())
	if err != nil {
		return &pb.UpdateDataSourceRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateDataSourceRsp{RetInfo: response.Success("success"), DataSource: updated}, nil
}

func (s *Service) GetDataSource(ctx context.Context, req *pb.GetDataSourceReq) (*pb.GetDataSourceRsp, error) {
	item, err := s.metadata.GetDataSource(ctx, req.GetSpaceId(), req.GetDataSourceId())
	if err != nil {
		return &pb.GetDataSourceRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.GetDataSourceRsp{RetInfo: response.Success("success"), DataSource: item}, nil
}

func (s *Service) ListDataSources(ctx context.Context, req *pb.ListDataSourcesReq) (*pb.ListDataSourcesRsp, error) {
	items, page, err := s.metadata.ListDataSources(ctx, req.GetSpaceId(), req.GetKind(), req.GetMarket(), req.GetPage())
	if err != nil {
		return &pb.ListDataSourcesRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
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
		return &pb.UpsertSubjectRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpsertSubjectRsp{RetInfo: response.Success("success"), Subject: created}, nil
}

func (s *Service) RegisterDataSubject(ctx context.Context, req *pb.RegisterDataSubjectReq) (*pb.RegisterDataSubjectRsp, error) {
	item := req.GetSubject()
	if req == nil || req.GetSpaceId() == "" || req.GetDataSourceId() == "" || req.GetExternalSymbol() == "" || item == nil || item.GetSubjectId() == "" {
		return &pb.RegisterDataSubjectRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, data_source_id, external_symbol and subject.subject_id are required"))}, nil
	}
	for _, binding := range req.GetDatasetBindings() {
		if binding == nil || binding.GetDatasetId() == "" {
			return &pb.RegisterDataSubjectRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset_bindings.dataset_id is required"))}, nil
		}
		if _, err := s.metadata.GetDataset(ctx, req.GetSpaceId(), binding.GetDatasetId()); err != nil {
			return &pb.RegisterDataSubjectRsp{RetInfo: response.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
		}
	}
	item.SpaceId = req.GetSpaceId()
	if item.Status == "" {
		item.Status = "active"
	}
	created, err := s.metadata.UpsertSubject(ctx, item)
	if err != nil {
		return &pb.RegisterDataSubjectRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	_, err = s.metadata.UpsertSubjectSymbol(ctx, &pb.SubjectSymbol{
		SpaceId:        req.GetSpaceId(),
		SubjectId:      created.GetSubjectId(),
		DataSourceId:   req.GetDataSourceId(),
		ExternalSymbol: req.GetExternalSymbol(),
		Status:         "active",
	})
	if err != nil {
		return &pb.RegisterDataSubjectRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}

	bindings := make([]*pb.DatasetSubject, 0, len(req.GetDatasetBindings()))
	for _, binding := range req.GetDatasetBindings() {
		binding.SpaceId = req.GetSpaceId()
		binding.SubjectId = created.GetSubjectId()
		if binding.SubjectRole == "" {
			binding.SubjectRole = "normal"
		}
		if binding.Status == "" {
			binding.Status = "active"
		}
		createdBinding, err := s.metadata.BindDatasetSubject(ctx, binding)
		if err != nil {
			return &pb.RegisterDataSubjectRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
		}
		bindings = append(bindings, createdBinding)
	}
	return &pb.RegisterDataSubjectRsp{RetInfo: response.Success("success"), Subject: created, DatasetBindings: bindings}, nil
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
		return &pb.ListSubjectsRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
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
		return &pb.UpsertSubjectSymbolRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpsertSubjectSymbolRsp{RetInfo: response.Success("success"), SubjectSymbol: created}, nil
}

func (s *Service) ListSubjectSymbols(ctx context.Context, req *pb.ListSubjectSymbolsReq) (*pb.ListSubjectSymbolsRsp, error) {
	items, page, err := s.metadata.ListSubjectSymbols(ctx, req.GetSpaceId(), req.GetSubjectId(), req.GetDataSourceId(), req.GetExternalSymbol(), req.GetPage())
	if err != nil {
		return &pb.ListSubjectSymbolsRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
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
	if err := validateChineseDisplayName("dataset name", item.GetName()); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	if err := validateDatasetID(item.GetDatasetId()); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	created, err := s.metadata.UpsertDataset(ctx, item)
	if err != nil {
		return &pb.CreateDatasetRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateDatasetRsp{RetInfo: response.Success("success"), Dataset: created}, nil
}

func (s *Service) UpdateDataset(ctx context.Context, req *pb.UpdateDatasetReq) (*pb.UpdateDatasetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetDatasetId() == "" {
		return &pb.UpdateDatasetRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset_id is required"))}, nil
	}
	if err := validateChineseDisplayName("dataset name", item.GetName()); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	if err := validateDatasetID(item.GetDatasetId()); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	updated, err := s.metadata.UpsertDataset(ctx, item)
	if err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
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
		return &pb.ListDatasetsRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListDatasetsRsp{RetInfo: response.Success("success"), Datasets: items, PageResult: page}, nil
}

func (s *Service) BindDatasetSubject(ctx context.Context, req *pb.BindDatasetSubjectReq) (*pb.BindDatasetSubjectRsp, error) {
	item := req.GetDatasetSubject()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetSubjectId() == "" {
		return &pb.BindDatasetSubjectRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and subject_id are required"))}, nil
	}
	if _, err := s.metadata.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId()); err != nil {
		return &pb.BindDatasetSubjectRsp{RetInfo: response.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
	}
	created, err := s.metadata.BindDatasetSubject(ctx, item)
	if err != nil {
		return &pb.BindDatasetSubjectRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.BindDatasetSubjectRsp{RetInfo: response.Success("success"), DatasetSubject: created}, nil
}

func (s *Service) ListDatasetSubjects(ctx context.Context, req *pb.ListDatasetSubjectsReq) (*pb.ListDatasetSubjectsRsp, error) {
	items, page, err := s.metadata.ListDatasetSubjects(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSubjectId(), req.GetPage())
	if err != nil {
		return &pb.ListDatasetSubjectsRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListDatasetSubjectsRsp{RetInfo: response.Success("success"), DatasetSubjects: items, PageResult: page}, nil
}

func (s *Service) CreateField(ctx context.Context, req *pb.CreateFieldReq) (*pb.CreateFieldRsp, error) {
	created, err := s.metadata.UpsertField(ctx, req.GetField())
	if err != nil {
		return &pb.CreateFieldRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateFieldRsp{RetInfo: response.Success("success"), Field: created}, nil
}

func (s *Service) UpdateField(ctx context.Context, req *pb.UpdateFieldReq) (*pb.UpdateFieldRsp, error) {
	updated, err := s.metadata.UpsertField(ctx, req.GetField())
	if err != nil {
		return &pb.UpdateFieldRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
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
		return &pb.ListFieldsRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListFieldsRsp{RetInfo: response.Success("success"), Fields: items, PageResult: page}, nil
}

func (s *Service) CreateFactor(ctx context.Context, req *pb.CreateFactorReq) (*pb.CreateFactorRsp, error) {
	created, err := s.metadata.UpsertFactor(ctx, req.GetFactor())
	if err != nil {
		return &pb.CreateFactorRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateFactorRsp{RetInfo: response.Success("success"), Factor: created}, nil
}

func (s *Service) UpdateFactor(ctx context.Context, req *pb.UpdateFactorReq) (*pb.UpdateFactorRsp, error) {
	updated, err := s.metadata.UpsertFactor(ctx, req.GetFactor())
	if err != nil {
		return &pb.UpdateFactorRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
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
		return &pb.ListFactorsRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListFactorsRsp{RetInfo: response.Success("success"), Factors: items, PageResult: page}, nil
}

func (s *Service) UpsertDatasetColumn(ctx context.Context, req *pb.UpsertDatasetColumnReq) (*pb.UpsertDatasetColumnRsp, error) {
	item := req.GetColumn()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return &pb.UpsertDatasetColumnRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and column_name are required"))}, nil
	}
	if err := validateColumnDisplayName("dataset column display_name", item.GetAttributes()); err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	created, err := s.metadata.UpsertDatasetColumn(ctx, item)
	if err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpsertDatasetColumnRsp{RetInfo: response.Success("success"), Column: created}, nil
}

func (s *Service) ListDatasetColumns(ctx context.Context, req *pb.ListDatasetColumnsReq) (*pb.ListDatasetColumnsRsp, error) {
	items, page, err := s.metadata.ListDatasetColumns(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetPage())
	if err != nil {
		return &pb.ListDatasetColumnsRsp{RetInfo: response.Error(response.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListDatasetColumnsRsp{RetInfo: response.Success("success"), Columns: items, PageResult: page}, nil
}
