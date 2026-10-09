package catalog

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	trpc "trpc.group/trpc-go/trpc-go"
)

// 本文件聚合数据源、主体、数据集、字段、因子及其列绑定相关的元数据 CRUD 入口。

const factorDatasetDeleteAppID = "moox-factor"

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

func (s *Service) DeleteDataSource(ctx context.Context, req *pb.DeleteDataSourceReq) (*pb.DeleteDataSourceRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDataSourceId()) == "" {
		return &pb.DeleteDataSourceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and data_source_id are required"))}, nil
	}
	if err := s.metadata.DeleteDataSource(ctx, req.GetSpaceId(), req.GetDataSourceId()); err != nil {
		return &pb.DeleteDataSourceRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "DeleteDataSource")
	return &pb.DeleteDataSourceRsp{RetInfo: retinfo.Success("success")}, nil
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

func collectorOwnedDataset(item *pb.Dataset) bool {
	if item == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(item.GetAttributes()["owner_module"]), "collector")
}

func (s *Service) CreateDataset(ctx context.Context, req *pb.CreateDatasetReq) (*pb.CreateDatasetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetSpaceId() == "" || (item.GetDatasetId() == "" && item.GetName() == "") {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id or name are required"))}, nil
	}
	if strings.TrimSpace(item.GetDataSourceId()) == "" && !collectorOwnedDataset(item) {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("data_source_id is required unless owner_module is collector"))}, nil
	}
	if err := validateDatasetDataNodeID(item.GetDataNodeId()); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	item.DataNodeId = strings.TrimSpace(item.GetDataNodeId())
	if item.DatasetId == "" {
		item.DatasetId = defaultID(item.GetName(), "dataset")
		if !strings.HasPrefix(item.DatasetId, "dataset_") {
			item.DatasetId = "dataset_" + item.DatasetId
		}
	}
	if err := validateChineseDisplayName("dataset name", item.GetName()); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := validateDatasetID(item.GetDatasetId()); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := normalizeDatasetRole(item); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	created, err := s.metadata.UpsertDataset(ctx, item)
	if err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.CreateDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: s.withRetention(created)}, nil
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
	if err := normalizeDatasetRole(item); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	current, err := s.metadata.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId())
	if err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := validateDatasetDataNodeUpdate(current, item.GetDataNodeId()); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if err := coremetadata.PreserveDatasetOwnerAttributes(current, item); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	item.DataNodeId = strings.TrimSpace(item.GetDataNodeId())
	updated, err := s.metadata.UpsertDataset(ctx, item)
	if err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if err := s.refreshMetadataCache(ctx); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.UpdateDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: s.withRetention(updated)}, nil
}

func (s *Service) DeleteDataset(ctx context.Context, req *pb.DeleteDatasetReq) (*pb.DeleteDatasetRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return &pb.DeleteDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	current, err := s.metadata.GetDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.DeleteDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if isFactorResultDataset(current) {
		if err := s.validateFactorDatasetDeleteAuth(req.GetAuthInfo()); err != nil {
			return &pb.DeleteDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
		}
	} else if collectorOwnedDataset(current) {
		if err := s.validateCollectorDatasetDeleteAuth(req.GetAuthInfo()); err != nil {
			return &pb.DeleteDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
		}
	}
	if err := s.metadata.DeleteDataset(ctx, req.GetSpaceId(), req.GetDatasetId()); err != nil {
		return &pb.DeleteDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "DeleteDataset")
	return &pb.DeleteDatasetRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) validateFactorDatasetDeleteAuth(auth *pb.AuthInfo) error {
	if strings.TrimSpace(s.operatorSecret) == "" {
		return errors.New("storage auth secret is not configured")
	}
	if auth == nil || strings.TrimSpace(auth.GetAppId()) == "" || strings.TrimSpace(auth.GetAppKey()) == "" {
		return errors.New("factor service auth is required")
	}
	if auth.GetAppId() != factorDatasetDeleteAppID {
		return errors.New("factor service identity required")
	}
	expected := serviceAuthKey(s.operatorSecret, factorDatasetDeleteAppID)
	if !hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(auth.GetAppKey()))), []byte(expected)) {
		return errors.New("invalid factor service HMAC")
	}
	return nil
}

func (s *Service) validateCollectorDatasetDeleteAuth(auth *pb.AuthInfo) error {
	if strings.TrimSpace(s.operatorSecret) == "" {
		return errors.New("storage auth secret is not configured")
	}
	if auth == nil || strings.TrimSpace(auth.GetAppId()) == "" || strings.TrimSpace(auth.GetAppKey()) == "" {
		return errors.New("collector service auth is required")
	}
	appID := strings.TrimSpace(auth.GetAppId())
	if appID != "moox-collector" {
		return errors.New("collector service identity required")
	}
	expected := serviceAuthKey(s.operatorSecret, appID)
	if !hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(auth.GetAppKey()))), []byte(expected)) {
		return errors.New("invalid collector service HMAC")
	}
	return nil
}

func (s *Service) GetDataset(ctx context.Context, req *pb.GetDatasetReq) (*pb.GetDatasetRsp, error) {
	item, err := s.metadata.GetDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.GetDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
	}
	return &pb.GetDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: s.withRetention(item)}, nil
}

func (s *Service) ListDatasets(ctx context.Context, req *pb.ListDatasetsReq) (*pb.ListDatasetsRsp, error) {
	query := coremetadata.DatasetQuery{
		SpaceID:      req.GetSpaceId(),
		DataSourceID: req.GetDataSourceId(),
		DataNodeID:   req.GetDataNodeId(),
		DataKind:     req.GetDataKind(),
		Freq:         req.GetFreq(),
		Page:         req.GetPage(),
	}
	items, page, err := s.metadata.ListDatasets(ctx, query)
	if err != nil {
		return &pb.ListDatasetsRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	return &pb.ListDatasetsRsp{RetInfo: retinfo.Success("success"), Datasets: s.withRetentions(items), PageResult: page}, nil
}

func (s *Service) RebindDatasetDataNode(ctx context.Context, req *pb.RebindDatasetDataNodeReq) (*pb.RebindDatasetDataNodeRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" || strings.TrimSpace(req.GetDataNodeId()) == "" || req.GetExpectedRevision() == 0 {
		return &pb.RebindDatasetDataNodeRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id, data_node_id and expected_revision are required"))}, nil
	}
	current, err := s.metadata.GetDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.RebindDatasetDataNodeRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if current.GetStatus() != "disabled" {
		return &pb.RebindDatasetDataNodeRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset must be disabled before offline data node rebind"))}, nil
	}
	dataset, err := s.metadata.RebindDatasetDataNode(ctx, req.GetSpaceId(), req.GetDatasetId(), strings.TrimSpace(req.GetDataNodeId()), req.GetExpectedRevision())
	if err != nil {
		return &pb.RebindDatasetDataNodeRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	s.refreshMetadataCacheAfterCommit(ctx, "RebindDatasetDataNode")
	return &pb.RebindDatasetDataNodeRsp{RetInfo: retinfo.Success("success"), Dataset: s.withRetention(dataset)}, nil
}

func (s *Service) CheckDatasetActivation(ctx context.Context, req *pb.CheckDatasetActivationReq) (*pb.CheckDatasetActivationRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return &pb.CheckDatasetActivationRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	dataset, err := s.metadata.GetDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.CheckDatasetActivationRsp{RetInfo: datasetReadRetInfo(err)}, nil
	}
	checks := newActivationChecker(s.metadata, s.nodeState, s.nodeAuthSecret, s.retention).checks(ctx, dataset)
	return &pb.CheckDatasetActivationRsp{
		RetInfo:         retinfo.Success("success"),
		DatasetRevision: dataset.GetRevision(),
		Checks:          checks,
		Ready:           activationReady(checks),
	}, nil
}

func (s *Service) ActivateDataset(ctx context.Context, req *pb.ActivateDatasetReq) (*pb.ActivateDatasetRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" || req.GetExpectedRevision() == 0 {
		return &pb.ActivateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and expected_revision are required"))}, nil
	}
	dataset, err := s.metadata.GetDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.ActivateDatasetRsp{RetInfo: datasetReadRetInfo(err)}, nil
	}
	if err := validateFactorResultDatasetContract(dataset); err != nil {
		return &pb.ActivateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	// An active, locked Dataset is the successful terminal state. Return it
	// before readiness checks so retries remain idempotent even when the node
	// is temporarily disabled or unreachable. factor_result View creation is
	// retried here too because it follows the Dataset activation transaction.
	if dataset.GetStatus() == "active" && dataset.GetBindingLocked() {
		if err := s.ensureFactorResultDefaultView(ctx, dataset); err != nil {
			return &pb.ActivateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("Dataset is active but its default factor View is pending; retry activation")), Dataset: dataset}, nil
		}
		return &pb.ActivateDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: s.withRetention(dataset)}, nil
	}
	checks := newActivationChecker(s.metadata, s.nodeState, s.nodeAuthSecret, s.retention).checks(ctx, dataset)
	if !activationReady(checks) {
		return &pb.ActivateDatasetRsp{
			RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("Dataset activation checks failed")),
			Checks:  checks,
		}, nil
	}
	if dataset.GetRevision() != req.GetExpectedRevision() {
		return &pb.ActivateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_CONFLICT, errors.New("dataset revision conflict")), Checks: checks}, nil
	}
	activated, err := s.metadata.CommitDatasetActivation(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetExpectedRevision())
	if err != nil {
		return &pb.ActivateDatasetRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err), Checks: checks}, nil
	}
	if err := s.ensureFactorResultDefaultView(ctx, activated); err != nil {
		return &pb.ActivateDatasetRsp{
			RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("Dataset activated but its default factor View is pending; retry activation")),
			Dataset: s.withRetention(activated), Checks: checks,
		}, nil
	}
	if err := s.refreshMetadataCacheSynchronously(ctx, "ActivateDataset"); err != nil {
		return &pb.ActivateDatasetRsp{
			RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("Dataset activated but metadata publication is pending; retry activation")),
			Dataset: s.withRetention(activated),
			Checks:  checks,
		}, nil
	}
	return &pb.ActivateDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: s.withRetention(activated), Checks: checks}, nil
}

func datasetReadRetInfo(err error) *pb.RetInfo {
	if errors.Is(err, sql.ErrNoRows) {
		return retinfo.Error(pb.ErrorCode_DATASET_NOT_FOUND, errors.New("Dataset not found"))
	}
	code := retinfo.MetadataStoreCode(err)
	if code == pb.ErrorCode_NOT_FOUND {
		code = pb.ErrorCode_INNER_ERR
	}
	return retinfo.Error(code, errors.New("Dataset metadata could not be read"))
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
	if err := validateUserColumnName("field_id", item.GetFieldId()); err != nil {
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
	if err := validateUserColumnName("field_id", item.GetFieldId()); err != nil {
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

// validateFieldSpaceContext 校验调用方在 tRPC 元数据 x-space-id 中声明的空间与请求一致。控制台转发时总会
// 写入它；内部组件不带时不校验。
func validateFieldSpaceContext(ctx context.Context, requestSpaceID string) error {
	scoped := strings.TrimSpace(string(trpc.GetMetaData(ctx, gatewayroute.MetadataSpaceID)))
	if scoped == "" {
		return nil
	}
	if requestSpaceID == "" || requestSpaceID != scoped {
		return fmt.Errorf("request space_id does not match %s metadata", gatewayroute.MetadataSpaceID)
	}
	return nil
}

func (s *Service) UpsertDatasetColumn(ctx context.Context, req *pb.UpsertDatasetColumnReq) (*pb.UpsertDatasetColumnRsp, error) {
	item := req.GetColumn()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and column_name are required"))}, nil
	}
	dataset, err := s.metadata.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId())
	if err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	factorResult := isFactorResultDataset(dataset)
	factorOutput := factorResult && item.GetOriginType() == pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR
	if err := validateColumnDisplayName("dataset column display_name", item.GetSpaceId(), item.GetAttributes(), factorOutput); err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(retinfo.MetadataStoreCode(err), err)}, nil
	}
	if factorOutput {
		attrs := item.GetAttributes()
		factorID := strings.TrimSpace(attrs["origin_factor_id"])
		output := strings.TrimSpace(attrs["factor_output"])
		if factorID == "" || factorID != strings.TrimSpace(item.GetOriginId()) || output != strings.TrimSpace(item.GetColumnName()) {
			err := errors.New("factor result columns require matching origin_factor_id and factor_output")
			return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
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
