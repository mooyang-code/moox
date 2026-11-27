package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 本文件聚合数据源、主体、数据集及其列相关的元数据 CRUD 入口。

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
		return &pb.CreateDataSourceRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.CreateDataSourceRsp{RetInfo: retinfo.Success("success"), DataSource: created}, nil
}

func (s *Service) UpdateDataSource(ctx context.Context, req *pb.UpdateDataSourceReq) (*pb.UpdateDataSourceRsp, error) {
	updated, err := s.metadata.UpsertDataSource(ctx, req.GetDataSource())
	if err != nil {
		return &pb.UpdateDataSourceRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.UpdateDataSourceRsp{RetInfo: retinfo.Success("success"), DataSource: updated}, nil
}

func (s *Service) DeleteDataSource(ctx context.Context, req *pb.DeleteDataSourceReq) (*pb.DeleteDataSourceRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDataSourceId()) == "" {
		return &pb.DeleteDataSourceRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and data_source_id are required"))}, nil
	}
	if err := s.metadata.DeleteDataSource(ctx, req.GetSpaceId(), req.GetDataSourceId()); err != nil {
		return &pb.DeleteDataSourceRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.DeleteDataSourceRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) GetDataSource(ctx context.Context, req *pb.GetDataSourceReq) (*pb.GetDataSourceRsp, error) {
	item, err := s.metadata.GetDataSource(ctx, req.GetSpaceId(), req.GetDataSourceId())
	if err != nil {
		return &pb.GetDataSourceRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.GetDataSourceRsp{RetInfo: retinfo.Success("success"), DataSource: item}, nil
}

func (s *Service) ListDataSources(ctx context.Context, req *pb.ListDataSourcesReq) (*pb.ListDataSourcesRsp, error) {
	items, page, err := s.metadata.ListDataSources(ctx, req.GetSpaceId(), req.GetKeyword(), req.GetPage())
	if err != nil {
		return &pb.ListDataSourcesRsp{RetInfo: retinfo.FromError(err)}, nil
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
	created, err := s.metadata.UpsertSubject(ctx, item)
	if err != nil {
		return &pb.UpsertSubjectRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.UpsertSubjectRsp{RetInfo: retinfo.Success("success"), Subject: created}, nil
}

func (s *Service) GetSubject(ctx context.Context, req *pb.GetSubjectReq) (*pb.GetSubjectRsp, error) {
	item, err := s.metadata.GetSubject(ctx, req.GetSpaceId(), req.GetSubjectId())
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return &pb.GetSubjectRsp{RetInfo: retinfo.FromError(err)}, nil
		}
		return &pb.GetSubjectRsp{RetInfo: retinfo.Error(pb.ErrorCode_SUBJECT_NOT_FOUND, err)}, nil
	}
	return &pb.GetSubjectRsp{RetInfo: retinfo.Success("success"), Subject: item}, nil
}

func (s *Service) ListSubjects(ctx context.Context, req *pb.ListSubjectsReq) (*pb.ListSubjectsRsp, error) {
	items, page, err := s.metadata.ListSubjects(ctx, req.GetSpaceId(), req.GetSubjectIds(), req.GetKeyword(), req.GetPage())
	if err != nil {
		return &pb.ListSubjectsRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.ListSubjectsRsp{RetInfo: retinfo.Success("success"), Subjects: items, PageResult: page}, nil
}

func (s *Service) CreateDataset(ctx context.Context, req *pb.CreateDatasetReq) (*pb.CreateDatasetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetSpaceId() == "" || (item.GetDatasetId() == "" && item.GetName() == "") {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id or name are required"))}, nil
	}
	if item.DatasetId == "" {
		item.DatasetId = defaultID(item.GetName(), "dataset")
		if !strings.HasPrefix(item.DatasetId, "dataset_") {
			item.DatasetId = "dataset_" + item.DatasetId
		}
	}
	if err := validateChineseDisplayName("dataset name", item.GetName()); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	if err := validateDatasetID(item.GetDatasetId()); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	for _, column := range req.GetColumns() {
		if column == nil || strings.TrimSpace(column.GetColumnName()) == "" {
			return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("column_name is required"))}, nil
		}
		if err := validateDatasetColumnName(column.GetColumnName()); err != nil {
			return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		if err := validateColumnDisplayName("dataset column display_name", column.GetDisplayName()); err != nil {
			return &pb.CreateDatasetRsp{RetInfo: retinfo.FromError(err)}, nil
		}
	}
	created, err := s.metadata.UpsertDataset(ctx, item, req.GetColumns()...)
	if err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	// 建好即可写入：返回前把数据集缓存发布出去，写入方紧接着的写入才能在缓存里查到数据集和列。
	if err := s.refreshDatasetCache(ctx, "CreateDataset"); err != nil {
		return &pb.CreateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("数据集已创建，但元数据缓存尚未发布，请重试")), Dataset: s.withRetention(created)}, nil
	}
	return &pb.CreateDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: s.withRetention(created)}, nil
}

func (s *Service) UpdateDataset(ctx context.Context, req *pb.UpdateDatasetReq) (*pb.UpdateDatasetRsp, error) {
	item := req.GetDataset()
	if item == nil || item.GetDatasetId() == "" {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset_id is required"))}, nil
	}
	if err := validateChineseDisplayName("dataset name", item.GetName()); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	if err := validateDatasetID(item.GetDatasetId()); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	if _, err := s.metadata.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId()); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	updated, err := s.metadata.UpsertDataset(ctx, item)
	if err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	if err := s.refreshDatasetCache(ctx, "UpdateDataset"); err != nil {
		return &pb.UpdateDatasetRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.UpdateDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: s.withRetention(updated)}, nil
}

func (s *Service) DeleteDataset(ctx context.Context, req *pb.DeleteDatasetReq) (*pb.DeleteDatasetRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" {
		return &pb.DeleteDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if s.rows == nil {
		return &pb.DeleteDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("dataset row store is not configured"))}, nil
	}
	// 先删元数据并发布缓存，再在写锁里删行：写入拿到读锁后按最新缓存确认数据集仍在，删行之后进来的写入会被拒绝。
	// 元数据已经不在时照样删行，让中途失败的删除可以重试完成，返回的仍是“不存在”。
	metadataErr := s.metadata.DeleteDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if metadataErr != nil && !errors.Is(metadataErr, sql.ErrNoRows) {
		return &pb.DeleteDatasetRsp{RetInfo: retinfo.FromError(metadataErr)}, nil
	}
	if err := s.refreshDatasetCache(ctx, "DeleteDataset"); err != nil {
		return &pb.DeleteDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("数据集元数据已删除，但缓存尚未发布，请重试删除"))}, nil
	}
	if _, err := s.rows.DeleteDatasetRows(ctx, req.GetSpaceId(), req.GetDatasetId()); err != nil {
		return &pb.DeleteDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, fmt.Errorf("数据集元数据已删除，删除行失败，请重试删除：%w", err))}, nil
	}
	if metadataErr != nil {
		return &pb.DeleteDatasetRsp{RetInfo: retinfo.FromError(metadataErr)}, nil
	}
	return &pb.DeleteDatasetRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) GetDataset(ctx context.Context, req *pb.GetDatasetReq) (*pb.GetDatasetRsp, error) {
	item, err := s.metadata.GetDataset(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		// 只有确实不存在才报 DATASET_NOT_FOUND：调用方（例如删除结果数据集）据此认为“已经删掉”，读取出错不能混进来。
		if errors.Is(err, sql.ErrNoRows) {
			return &pb.GetDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_DATASET_NOT_FOUND, err)}, nil
		}
		return &pb.GetDatasetRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("Dataset metadata could not be read"))}, nil
	}
	revision, err := s.metadata.SubjectsRevision(ctx, req.GetSpaceId())
	if err != nil {
		return &pb.GetDatasetRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.GetDatasetRsp{RetInfo: retinfo.Success("success"), Dataset: s.withRetention(item), SubjectsRevision: revision}, nil
}

func (s *Service) ListDatasets(ctx context.Context, req *pb.ListDatasetsReq) (*pb.ListDatasetsRsp, error) {
	query := coremetadata.DatasetQuery{
		SpaceID:      req.GetSpaceId(),
		DataSourceID: req.GetDataSourceId(),
		DataKind:     req.GetDataKind(),
		Freq:         req.GetFreq(),
		Status:       req.GetStatus(),
		Page:         req.GetPage(),
	}
	items, page, err := s.metadata.ListDatasets(ctx, query)
	if err != nil {
		return &pb.ListDatasetsRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.ListDatasetsRsp{RetInfo: retinfo.Success("success"), Datasets: s.withRetentions(items), PageResult: page}, nil
}

func (s *Service) ListDatasetSubjects(ctx context.Context, req *pb.ListDatasetSubjectsReq) (*pb.ListDatasetSubjectsRsp, error) {
	items, page, err := s.metadata.ListDatasetSubjects(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetSubjectId(), req.GetPage())
	if err != nil {
		return &pb.ListDatasetSubjectsRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.ListDatasetSubjectsRsp{RetInfo: retinfo.Success("success"), DatasetSubjects: items, PageResult: page}, nil
}

func (s *Service) UpsertDatasetColumn(ctx context.Context, req *pb.UpsertDatasetColumnReq) (*pb.UpsertDatasetColumnRsp, error) {
	item := req.GetColumn()
	if item == nil || item.GetSpaceId() == "" || item.GetDatasetId() == "" || item.GetColumnName() == "" {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and column_name are required"))}, nil
	}
	if err := validateDatasetColumnName(item.GetColumnName()); err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	if _, err := s.metadata.GetDataset(ctx, item.GetSpaceId(), item.GetDatasetId()); err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	if err := validateColumnDisplayName("dataset column display_name", item.GetDisplayName()); err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	created, err := s.metadata.UpsertDatasetColumn(ctx, item)
	if err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	// 写入方补完列就会写行：返回前把列发布到缓存，否则首次写入可能因未登记的列被拒。
	if err := s.refreshDatasetCache(ctx, "UpsertDatasetColumn"); err != nil {
		return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("列已写入，缓存尚未发布，请重试")), Column: created}, nil
	}
	return &pb.UpsertDatasetColumnRsp{RetInfo: retinfo.Success("success"), Column: created}, nil
}

func (s *Service) ListDatasetColumns(ctx context.Context, req *pb.ListDatasetColumnsReq) (*pb.ListDatasetColumnsRsp, error) {
	items, page, err := s.metadata.ListDatasetColumns(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetPage())
	if err != nil {
		return &pb.ListDatasetColumnsRsp{RetInfo: retinfo.FromError(err)}, nil
	}
	return &pb.ListDatasetColumnsRsp{RetInfo: retinfo.Success("success"), Columns: items, PageResult: page}, nil
}
