package primarystore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type factorRowsDataNodeClient interface {
	WriteFactorRows(context.Context, *pb.WriteFactorRowsReq) (*pb.WriteFactorRowsRsp, error)
}

func (s *Service) WriteFactorRows(ctx context.Context, req *pb.PrimaryWriteFactorRowsReq) (*pb.PrimaryWriteFactorRowsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetDatasetId()) == "" || strings.TrimSpace(req.GetCommitId()) == "" || len(req.GetRows()) == 0 {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id, commit_id and rows are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if !isFactorAppID(req.GetAuthInfo().GetAppId()) {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("factor result write requires factor caller"))}, nil
	}

	ctx = s.requestContext(ctx)
	snapshot := metadata.RequestSnapshotFromContext(ctx)
	if snapshot == nil {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("metadata cache snapshot is unavailable"))}, nil
	}
	dataset, ok := snapshot.GetDataset(req.GetSpaceId(), req.GetDatasetId())
	if !ok || dataset == nil || dataset.GetSpaceId() != req.GetSpaceId() || dataset.GetDatasetId() != req.GetDatasetId() {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("factor result Dataset is not present in the request metadata snapshot"))}, nil
	}
	attrs := dataset.GetAttributes()
	if !strings.EqualFold(strings.TrimSpace(attrs["dataset_role"]), "factor_result") ||
		!strings.EqualFold(strings.TrimSpace(attrs["owner_module"]), "factor") ||
		!strings.EqualFold(strings.TrimSpace(attrs["write_owner"]), "factor") {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("factor writes require a factor-owned factor_result Dataset"))}, nil
	}
	if dataset.GetStatus() != "active" {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, fmt.Errorf("factor result Dataset is %q, writes require active", dataset.GetStatus()))}, nil
	}
	ownerNodeID := strings.TrimSpace(dataset.GetDataNodeId())
	if ownerNodeID == "" {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("factor result Dataset has no owner DataNode"))}, nil
	}

	columns, err := factorResultColumnNames(snapshot, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	rows := make([]*pb.RowFieldUpsert, len(req.GetRows()))
	for i, row := range req.GetRows() {
		if row == nil || row.GetKey() == nil || row.GetKey().GetSpaceId() != req.GetSpaceId() || row.GetKey().GetDatasetId() != req.GetDatasetId() {
			return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("row dataset identity does not match request"))}, nil
		}
		if len(row.GetFields()) == 0 {
			return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("factor result row fields are required"))}, nil
		}
		if len(row.GetAttributes()) != 0 {
			return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("factor result rows do not support row attributes"))}, nil
		}
		if err := validateRow(ctx, row, s.validate); err != nil {
			return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		for _, field := range row.GetFields() {
			if _, exists := columns[field.GetFieldId()]; !exists {
				return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, fmt.Errorf("field %q is not declared by factor result Dataset", field.GetFieldId()))}, nil
			}
		}
		rows[i] = row
	}

	node, err := s.resolve(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	dataNode, ok := node.(factorRowsDataNodeClient)
	if !ok {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode factor rows RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := dataNode.WriteFactorRows(ctx, &pb.WriteFactorRowsReq{
		AuthInfo: auth, NodeId: ownerNodeID, SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), CommitId: req.GetCommitId(), Rows: rows,
	})
	if err != nil {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return &pb.PrimaryWriteFactorRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode returned no factor rows response"))}, nil
	}
	return &pb.PrimaryWriteFactorRowsRsp{RetInfo: rsp.GetRetInfo(), RowsWritten: rsp.GetRowsWritten()}, nil
}

func factorResultColumnNames(snapshot metadata.RequestSnapshot, spaceID, datasetID string) (map[string]struct{}, error) {
	columns := make(map[string]struct{})
	const pageSize = uint32(1000)
	for pageNo := uint32(1); ; pageNo++ {
		items, page, err := snapshot.ListDatasetColumns(spaceID, datasetID, &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, fmt.Errorf("list factor result Dataset columns: %w", err)
		}
		for _, column := range items {
			if column == nil || (column.GetStatus() != "" && column.GetStatus() != "active") {
				continue
			}
			if column.GetColumnName() != "" {
				columns[column.GetColumnName()] = struct{}{}
			}
		}
		if page == nil || !page.GetHasMore() || len(items) == 0 {
			return columns, nil
		}
	}
}
