//go:build legacy_storage

package primarystore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

// 本文件聚合数据源、主体、数据集、字段、因子及其列绑定相关的元数据 CRUD 入口。

func (s *Service) CreateDataSource(ctx context.Context, req *pb.CreateDataSourceReq) (*pb.CreateDataSourceRsp, error) {
	item := req.GetDataSource()
	if item == nil || item.GetSpaceId() == "" || (item.GetDataSourceId() == "" && item.GetName() == "") {
		return &pb.CreateDataSourceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and data_source_id or name are required"))}, nil
	}
	if item.DataSourceId == "" {
		item.DataSourceId = defaultID(item.GetName(), "data_source")
	}
	if item.Name == "" {
		item.Name = item.GetDataSourceId()
	}
	created, err := s.metadata.UpsertDataSource(ctx, item)
	if err != nil {
		return &pb.CreateDataSourceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.CreateDataSourceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateDataSourceRsp{RetInfo: retinfo.Success("success"), DataSource: created}, nil
}

func (s *Service) UpdateDataSource(ctx context.Context, req *pb.UpdateDataSourceReq) (*pb.UpdateDataSourceRsp, error) {
	updated, err := s.metadata.UpsertDataSource(ctx, req.GetDataSource())
	if err != nil {
		return &pb.UpdateDataSourceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpdateDataSourceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateDataSourceRsp{RetInfo: retinfo.Success("success"), DataSource: updated}, nil
}

func (s *Service) GetDataSource(ctx context.Context, req *pb.GetDataSourceReq) (*pb.GetDataSourceRsp, error) {
	item, err := s.metadata.GetDataSource(ctx, req.GetSpaceId(), req.GetDataSourceId())
	if err != nil {
		return &pb.GetDataSourceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.GetDataSourceRsp{RetInfo: retinfo.Success("success"), DataSource: item}, nil
}

func (s *Service) ListDataSources(ctx context.Context, req *pb.ListDataSourcesReq) (*pb.ListDataSourcesRsp, error) {
	items, page, err := s.metadata.ListDataSources(ctx, req.GetSpaceId(), req.GetKind(), req.GetMarket(), req.GetKeyword(), req.GetPage())
	if err != nil {
		return &pb.ListDataSourcesRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListDataSourcesRsp{RetInfo: retinfo.Success("success"), DataSources: items, PageResult: page}, nil
}

func (s *Service) UpsertSubject(ctx context.Context, req *pb.UpsertSubjectReq) (*pb.UpsertSubjectRsp, error) {
	item := req.GetSubject()
	if item == nil || item.GetSpaceId() == "" || (item.GetSubjectId() == "" && item.GetName() == "") {
		return &pb.UpsertSubjectRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and subject_id or name are required"))}, nil
	}
	if item.SubjectId == "" {
		item.SubjectId = defaultID(item.GetName(), "subject")
	}
	if item.SubjectType == "" {
		item.SubjectType = "custom"
	}
	created, err := s.metadata.UpsertSubject(ctx, item)
	if err != nil {
		return &pb.UpsertSubjectRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpsertSubjectRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpsertSubjectRsp{RetInfo: retinfo.Success("success"), Subject: created}, nil
}

func (s *Service) RegisterDataSubject(ctx context.Context, req *pb.RegisterDataSubjectReq) (*pb.RegisterDataSubjectRsp, error) {
	item := req.GetSubject()
	if req == nil || req.GetSpaceId() == "" || req.GetDataSourceId() == "" || req.GetExternalSymbol() == "" || item == nil || item.GetSubjectId() == "" {
		return &pb.RegisterDataSubjectRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, data_source_id, external_symbol and subject.subject_id are required"))}, nil
	}
	for _, binding := range req.GetDatasetBindings() {
		if binding == nil || binding.GetDatasetId() == "" {
			return &pb.RegisterDataSubjectRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset_bindings.dataset_id is required"))}, nil
		}
	}
	item = proto.Clone(item).(*pb.Subject)
	item.SpaceId = req.GetSpaceId()
	if item.Status == "" {
		item.Status = "active"
	}
	if item.SubjectType == "" {
		item.SubjectType = "custom"
	}
	symbol := &pb.SubjectSymbol{
		SpaceId:        req.GetSpaceId(),
		SubjectId:      item.GetSubjectId(),
		DataSourceId:   req.GetDataSourceId(),
		ExternalSymbol: req.GetExternalSymbol(),
		Status:         "active",
	}

	bindings := make([]*pb.DatasetSubject, 0, len(req.GetDatasetBindings()))
	for _, binding := range req.GetDatasetBindings() {
		copied := proto.Clone(binding).(*pb.DatasetSubject)
		copied.SpaceId = req.GetSpaceId()
		copied.SubjectId = item.GetSubjectId()
		if copied.SubjectRole == "" {
			copied.SubjectRole = "normal"
		}
		if copied.Status == "" {
			copied.Status = "active"
		}
		bindings = append(bindings, copied)
	}
	created, bindings, err := s.metadata.RegisterDataSubject(ctx, item, symbol, bindings)
	if err != nil {
		code := retinfo.MetadataStoreCode(err)
		if code == pb.ErrorCode_NOT_FOUND {
			code = pb.ErrorCode_DATASET_NOT_FOUND
		}
		return &pb.RegisterDataSubjectRsp{RetInfo: retinfo.Error(code, err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.RegisterDataSubjectRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.RegisterDataSubjectRsp{RetInfo: retinfo.Success("success"), Subject: created, DatasetBindings: bindings}, nil
}

func (s *Service) GetSubject(ctx context.Context, req *pb.GetSubjectReq) (*pb.GetSubjectRsp, error) {
	item, err := s.metadata.GetSubject(ctx, req.GetSpaceId(), req.GetSubjectId())
	if err != nil {
		return &pb.GetSubjectRsp{RetInfo: retinfo.Error(pb.ErrorCode_SUBJECT_NOT_FOUND, err)}, nil
	}
	return &pb.GetSubjectRsp{RetInfo: retinfo.Success("success"), Subject: item}, nil
}

func (s *Service) ListSubjects(ctx context.Context, req *pb.ListSubjectsReq) (*pb.ListSubjectsRsp, error) {
	items, page, err := s.metadata.ListSubjects(ctx, req.GetSpaceId(), req.GetSubjectType(), req.GetMarket(), req.GetSubjectIds(), req.GetKeyword(), req.GetPage())
	if err != nil {
		return &pb.ListSubjectsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListSubjectsRsp{RetInfo: retinfo.Success("success"), Subjects: items, PageResult: page}, nil
}

func (s *Service) UpsertSubjectSymbol(ctx context.Context, req *pb.UpsertSubjectSymbolReq) (*pb.UpsertSubjectSymbolRsp, error) {
	item := req.GetSubjectSymbol()
	if item == nil || item.GetSpaceId() == "" || item.GetSubjectId() == "" || item.GetDataSourceId() == "" || item.GetExternalSymbol() == "" {
		return &pb.UpsertSubjectSymbolRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, subject_id, data_source_id and external_symbol are required"))}, nil
	}
	created, err := s.metadata.UpsertSubjectSymbol(ctx, item)
	if err != nil {
		return &pb.UpsertSubjectSymbolRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpsertSubjectSymbolRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpsertSubjectSymbolRsp{RetInfo: retinfo.Success("success"), SubjectSymbol: created}, nil
}

func (s *Service) ListSubjectSymbols(ctx context.Context, req *pb.ListSubjectSymbolsReq) (*pb.ListSubjectSymbolsRsp, error) {
	items, page, err := s.metadata.ListSubjectSymbols(ctx, req.GetSpaceId(), req.GetSubjectId(), req.GetDataSourceId(), req.GetExternalSymbol(), req.GetPage())
	if err != nil {
		return &pb.ListSubjectSymbolsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListSubjectSymbolsRsp{RetInfo: retinfo.Success("success"), SubjectSymbols: items, PageResult: page}, nil
}

func (s *Service) CreateDataset(ctx context.Context, req *pb.CreateDatasetReq) (*pb.CreateDatasetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetSpaceId() == "" || item.GetDataSourceId() == "" || (item.GetDatasetId() == "" && item.GetName() == "") {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, data_source_id and dataset_id or name are required"))}, nil
	}
	if item.DatasetId == "" {
		item.DatasetId = defaultID(item.GetName(), "dataset")
	}
	if err := validateChineseDisplayName("dataset name", item.GetName()); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := validateDatasetID(item.GetDatasetId()); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	created, err := s.metadata.UpsertDataset(ctx, item)
	if err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: created}, nil
}

func (s *Service) UpdateDataset(ctx context.Context, req *pb.UpdateDatasetReq) (*pb.UpdateDatasetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetDatasetId() == "" {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset_id is required"))}, nil
	}
	if err := validateChineseDisplayName("dataset name", item.GetName()); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := validateDatasetID(item.GetDatasetId()); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	updated, err := s.metadata.UpsertDataset(ctx, item)
	if err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: updated}, nil
}

func (s *Service) GetDataset(ctx context.Context, req *pb.GetDatasetReq) (*pb.GetDatasetRsp, error) {
	item, err := s.metadata.GetDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.GetDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
	}
	return &pb.GetDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: item}, nil
}

func (s *Service) ListDatasets(ctx context.Context, req *pb.ListDatasetsReq) (*pb.ListDatasetsRsp, error) {
	items, page, err := s.metadata.ListDatasets(ctx, req.GetSpaceId(), req.GetDataSourceId(), req.GetDataKind(), req.GetFreq(), req.GetPage())
	if err != nil {
		return &pb.ListDatasetsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListDatasetsRsp{RetInfo: retinfo.Success("success"), Datasets: items, PageResult: page}, nil
}

func (s *Service) BindDatasetSubject(ctx context.Context, req *pb.BindDatasetSubjectReq) (*pb.BindDatasetSubjectRsp, error) {
	item := req.GetDatasetSubject()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetSubjectId() == "" {
		return &pb.BindDatasetSubjectRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and subject_id are required"))}, nil
	}
	if _, err := s.metadata.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId()); err != nil {
		return &pb.BindDatasetSubjectRsp{RetInfo: retinfo.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
	}
	created, err := s.metadata.BindDatasetSubject(ctx, item)
	if err != nil {
		return &pb.BindDatasetSubjectRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.BindDatasetSubjectRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.BindDatasetSubjectRsp{RetInfo: retinfo.Success("success"), DatasetSubject: created}, nil
}

func (s *Service) ListDatasetSubjects(ctx context.Context, req *pb.ListDatasetSubjectsReq) (*pb.ListDatasetSubjectsRsp, error) {
	items, page, err := s.metadata.ListDatasetSubjects(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSubjectId(), req.GetPage())
	if err != nil {
		return &pb.ListDatasetSubjectsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListDatasetSubjectsRsp{RetInfo: retinfo.Success("success"), DatasetSubjects: items, PageResult: page}, nil
}

func (s *Service) CreateFieldGroup(ctx context.Context, req *pb.CreateFieldGroupReq) (*pb.CreateFieldGroupRsp, error) {
	item := req.GetFieldGroup()
	if item == nil || item.GetSpaceId() == "" || item.GetName() == "" {
		return &pb.CreateFieldGroupRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and name are required"))}, nil
	}
	if item.GroupId == "" {
		item.GroupId = defaultID(item.GetName(), "field_group")
	}
	if err := validateFieldSpaceContext(ctx, item.GetSpaceId()); err != nil {
		return &pb.CreateFieldGroupRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	created, err := s.metadata.CreateFieldGroup(ctx, item)
	if err != nil {
		return &pb.CreateFieldGroupRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "create field group")
	return &pb.CreateFieldGroupRsp{RetInfo: retinfo.Success("success"), FieldGroup: created}, nil
}

func (s *Service) UpdateFieldGroup(ctx context.Context, req *pb.UpdateFieldGroupReq) (*pb.UpdateFieldGroupRsp, error) {
	item := req.GetFieldGroup()
	if item == nil || item.GetSpaceId() == "" || item.GetGroupId() == "" {
		return &pb.UpdateFieldGroupRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and group_id are required"))}, nil
	}
	if err := validateFieldSpaceContext(ctx, item.GetSpaceId()); err != nil {
		return &pb.UpdateFieldGroupRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	updated, err := s.metadata.UpdateFieldGroup(ctx, item)
	if err != nil {
		return &pb.UpdateFieldGroupRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "update field group")
	return &pb.UpdateFieldGroupRsp{RetInfo: retinfo.Success("success"), FieldGroup: updated}, nil
}

func (s *Service) GetFieldGroup(ctx context.Context, req *pb.GetFieldGroupReq) (*pb.GetFieldGroupRsp, error) {
	if err := validateFieldSpaceContext(ctx, req.GetSpaceId()); err != nil {
		return &pb.GetFieldGroupRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	item, err := s.metadata.GetFieldGroup(ctx, req.GetSpaceId(), req.GetGroupId())
	if err != nil {
		return &pb.GetFieldGroupRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.GetFieldGroupRsp{RetInfo: retinfo.Success("success"), FieldGroup: item}, nil
}

func (s *Service) ListFieldGroups(ctx context.Context, req *pb.ListFieldGroupsReq) (*pb.ListFieldGroupsRsp, error) {
	if err := validateFieldSpaceContext(ctx, req.GetSpaceId()); err != nil {
		return &pb.ListFieldGroupsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	items, page, err := s.metadata.ListFieldGroups(ctx, req.GetSpaceId(), req.GetParentGroupId(), req.GetPage())
	if err != nil {
		return &pb.ListFieldGroupsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	counts, err := s.metadata.CountFieldsByGroup(ctx, req.GetSpaceId())
	if err != nil {
		return &pb.ListFieldGroupsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListFieldGroupsRsp{
		RetInfo: retinfo.Success("success"), FieldGroups: items, PageResult: page,
		FieldCounts: counts.ByGroup, TotalFieldCount: counts.Total, UngroupedFieldCount: counts.Ungrouped,
	}, nil
}

func (s *Service) CreateField(ctx context.Context, req *pb.CreateFieldReq) (*pb.CreateFieldRsp, error) {
	if req.GetField() == nil || req.GetField().GetGroupId() == "" {
		return &pb.CreateFieldRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("field.group_id is required"))}, nil
	}
	item := req.GetField()
	if err := validateFieldSpaceContext(ctx, item.GetSpaceId()); err != nil {
		return &pb.CreateFieldRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	created, err := s.metadata.CreateField(ctx, item)
	if err != nil {
		return &pb.CreateFieldRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "create field")
	return &pb.CreateFieldRsp{RetInfo: retinfo.Success("success"), Field: created}, nil
}

func (s *Service) UpdateField(ctx context.Context, req *pb.UpdateFieldReq) (*pb.UpdateFieldRsp, error) {
	item := req.GetField()
	if item == nil || item.GetSpaceId() == "" || item.GetFieldId() == "" {
		return &pb.UpdateFieldRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and field_id are required"))}, nil
	}
	if err := validateFieldSpaceContext(ctx, item.GetSpaceId()); err != nil {
		return &pb.UpdateFieldRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	updated, err := s.metadata.UpdateField(ctx, item)
	if err != nil {
		return &pb.UpdateFieldRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "update field")
	return &pb.UpdateFieldRsp{RetInfo: retinfo.Success("success"), Field: updated}, nil
}

func (s *Service) GetField(ctx context.Context, req *pb.GetFieldReq) (*pb.GetFieldRsp, error) {
	if err := validateFieldSpaceContext(ctx, req.GetSpaceId()); err != nil {
		return &pb.GetFieldRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	item, err := s.metadata.GetField(ctx, req.GetSpaceId(), req.GetFieldId())
	if err != nil {
		return &pb.GetFieldRsp{RetInfo: retinfo.Error(pb.ErrorCode_FIELD_NOT_FOUND, err)}, nil
	}
	return &pb.GetFieldRsp{RetInfo: retinfo.Success("success"), Field: item}, nil
}

func (s *Service) ListFields(ctx context.Context, req *pb.ListFieldsReq) (*pb.ListFieldsRsp, error) {
	if err := validateFieldSpaceContext(ctx, req.GetSpaceId()); err != nil {
		return &pb.ListFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if req.GetGroupId() != "" && req.GetUngroupedOnly() {
		return &pb.ListFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("group_id and ungrouped_only cannot be used together"))}, nil
	}
	if !validFieldSort(req.GetSortBy(), req.GetSortOrder()) {
		return &pb.ListFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("invalid field sort"))}, nil
	}
	items, page, err := s.metadata.ListFields(ctx, coremetadata.FieldQuery{
		SpaceID: req.GetSpaceId(), GroupID: req.GetGroupId(), ValueType: req.GetValueType(),
		Status: req.GetStatus(), Keyword: req.GetKeyword(), IncludeDescendants: req.GetIncludeDescendants(),
		UngroupedOnly: req.GetUngroupedOnly(), SortBy: req.GetSortBy(), SortOrder: req.GetSortOrder(), Page: req.GetPage(),
	})
	if err != nil {
		return &pb.ListFieldsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListFieldsRsp{RetInfo: retinfo.Success("success"), Fields: items, PageResult: page}, nil
}

func validFieldSort(sortBy string, sortOrder string) bool {
	if sortBy != "" && sortBy != "sort_order" && sortBy != "field_id" && sortBy != "updated_at" {
		return false
	}
	return sortOrder == "" || strings.EqualFold(sortOrder, "asc") || strings.EqualFold(sortOrder, "desc")
}

func (s *Service) BatchUpdateFields(ctx context.Context, req *pb.BatchUpdateFieldsReq) (*pb.BatchUpdateFieldsRsp, error) {
	if req.GetSpaceId() == "" || len(req.GetFieldIds()) == 0 || len(req.GetFieldIds()) > 100 {
		return &pb.BatchUpdateFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and 1-100 field_ids are required"))}, nil
	}
	if err := validateFieldSpaceContext(ctx, req.GetSpaceId()); err != nil {
		return &pb.BatchUpdateFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if req.GetTargetGroupId() == "" && req.GetTargetStatus() == "" {
		return &pb.BatchUpdateFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("target_group_id or target_status is required"))}, nil
	}
	if req.GetTargetStatus() != "" && req.GetTargetStatus() != "active" && req.GetTargetStatus() != "disabled" {
		return &pb.BatchUpdateFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("target_status must be active or disabled"))}, nil
	}
	updated, err := s.metadata.BatchUpdateFields(ctx, req.GetSpaceId(), req.GetFieldIds(), req.GetTargetGroupId(), req.GetTargetStatus())
	if err != nil {
		return &pb.BatchUpdateFieldsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "batch update fields")
	return &pb.BatchUpdateFieldsRsp{RetInfo: retinfo.Success("success"), UpdatedCount: updated}, nil
}

func (s *Service) DeleteFieldGroup(ctx context.Context, req *pb.DeleteFieldGroupReq) (*pb.DeleteFieldGroupRsp, error) {
	if req.GetSpaceId() == "" || req.GetGroupId() == "" {
		return &pb.DeleteFieldGroupRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and group_id are required"))}, nil
	}
	if err := validateFieldSpaceContext(ctx, req.GetSpaceId()); err != nil {
		return &pb.DeleteFieldGroupRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if err := s.metadata.DeleteFieldGroup(ctx, req.GetSpaceId(), req.GetGroupId()); err != nil {
		return &pb.DeleteFieldGroupRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "delete field group")
	return &pb.DeleteFieldGroupRsp{RetInfo: retinfo.Success("success")}, nil
}

func validateFieldSpaceContext(ctx context.Context, requestSpaceID string) error {
	head := thttp.Head(ctx)
	if head == nil || head.Request == nil {
		return nil
	}
	headerSpaceID := strings.TrimSpace(head.Request.Header.Get("X-Space-Id"))
	if headerSpaceID == "" {
		return fmt.Errorf("%s header is required", http.CanonicalHeaderKey("X-Space-Id"))
	}
	if requestSpaceID == "" || requestSpaceID != headerSpaceID {
		return fmt.Errorf("request space_id does not match %s header", http.CanonicalHeaderKey("X-Space-Id"))
	}
	return nil
}

func (s *Service) CreateFactor(ctx context.Context, req *pb.CreateFactorReq) (*pb.CreateFactorRsp, error) {
	created, err := s.metadata.UpsertFactor(ctx, req.GetFactor())
	if err != nil {
		return &pb.CreateFactorRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.CreateFactorRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateFactorRsp{RetInfo: retinfo.Success("success"), Factor: created}, nil
}

func (s *Service) UpdateFactor(ctx context.Context, req *pb.UpdateFactorReq) (*pb.UpdateFactorRsp, error) {
	updated, err := s.metadata.UpsertFactor(ctx, req.GetFactor())
	if err != nil {
		return &pb.UpdateFactorRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpdateFactorRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateFactorRsp{RetInfo: retinfo.Success("success"), Factor: updated}, nil
}

func (s *Service) GetFactor(ctx context.Context, req *pb.GetFactorReq) (*pb.GetFactorRsp, error) {
	item, err := s.metadata.GetFactor(ctx, req.GetSpaceId(), req.GetFactorId())
	if err != nil {
		return &pb.GetFactorRsp{RetInfo: retinfo.Error(pb.ErrorCode_FACTOR_NOT_FOUND, err)}, nil
	}
	return &pb.GetFactorRsp{RetInfo: retinfo.Success("success"), Factor: item}, nil
}

func (s *Service) ListFactors(ctx context.Context, req *pb.ListFactorsReq) (*pb.ListFactorsRsp, error) {
	items, page, err := s.metadata.ListFactors(ctx, req.GetSpaceId(), req.GetAlgorithm(), req.GetPage())
	if err != nil {
		return &pb.ListFactorsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListFactorsRsp{RetInfo: retinfo.Success("success"), Factors: items, PageResult: page}, nil
}

func (s *Service) UpsertDatasetColumn(ctx context.Context, req *pb.UpsertDatasetColumnReq) (*pb.UpsertDatasetColumnRsp, error) {
	item := req.GetColumn()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and column_name are required"))}, nil
	}
	if err := validateColumnDisplayName("dataset column display_name", item.GetSpaceId(), item.GetAttributes()); err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	created, err := s.metadata.UpsertDatasetColumn(ctx, item)
	if err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Success("success"), Column: created}, nil
}

func (s *Service) ListDatasetColumns(ctx context.Context, req *pb.ListDatasetColumnsReq) (*pb.ListDatasetColumnsRsp, error) {
	items, page, err := s.metadata.ListDatasetColumns(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetPage())
	if err != nil {
		return &pb.ListDatasetColumnsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListDatasetColumnsRsp{RetInfo: retinfo.Success("success"), Columns: items, PageResult: page}, nil
}
