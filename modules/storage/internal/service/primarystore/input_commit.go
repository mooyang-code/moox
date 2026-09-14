package primarystore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type inputCommitDataNodeClient interface {
	CommitInput(context.Context, *pb.CommitInputReq) (*pb.CommitInputRsp, error)
	PatchFactor(context.Context, *pb.PatchFactorReq) (*pb.PatchFactorRsp, error)
	LookupWriteReceipt(context.Context, *pb.LookupWriteReceiptReq) (*pb.LookupWriteReceiptRsp, error)
}

func (s *Service) CommitInput(ctx context.Context, req *pb.PrimaryCommitInputReq) (*pb.PrimaryCommitInputRsp, error) {
	if req == nil || req.GetRow() == nil {
		return &pb.PrimaryCommitInputRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("row is required"))}, nil
	}
	if err := rejectMooxSkillWrite(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryCommitInputRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryCommitInputRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if !isMergeAppID(req.GetAuthInfo().GetAppId()) {
		return &pb.PrimaryCommitInputRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("input commit requires merge caller"))}, nil
	}
	rows := normalizeStockCNSeriesTags([]*pb.RowFieldUpsert{req.GetRow()})
	ctx = s.requestContext(ctx)
	if err := validateRow(ctx, rows[0], s.validate); err != nil {
		return &pb.PrimaryCommitInputRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	node, err := s.resolveInputCommitNode(ctx, rows[0].GetKey())
	if err != nil {
		return &pb.PrimaryCommitInputRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.PrimaryCommitInputRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := node.CommitInput(ctx, &pb.CommitInputReq{
		AuthInfo: auth, CommitId: req.GetCommitId(), RequiredFields: req.GetRequiredFields(), Row: rows[0],
	})
	if err != nil {
		return &pb.PrimaryCommitInputRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return &pb.PrimaryCommitInputRsp{RetInfo: rsp.GetRetInfo()}, nil
	}
	return &pb.PrimaryCommitInputRsp{RetInfo: retinfo.Success("success"), Receipt: rsp.GetReceipt()}, nil
}

func (s *Service) PatchFactor(ctx context.Context, req *pb.PrimaryPatchFactorReq) (*pb.PrimaryPatchFactorRsp, error) {
	if req == nil || req.GetRow() == nil {
		return &pb.PrimaryPatchFactorRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("row is required"))}, nil
	}
	if err := rejectMooxSkillWrite(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryPatchFactorRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryPatchFactorRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if !isFactorAppID(req.GetAuthInfo().GetAppId()) {
		return &pb.PrimaryPatchFactorRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, errors.New("factor patch requires factor caller"))}, nil
	}
	rows := normalizeStockCNSeriesTags([]*pb.RowFieldUpsert{req.GetRow()})
	ctx = s.requestContext(ctx)
	if err := validateDatasetWriteOwner(ctx, req.GetAuthInfo(), rows); err != nil {
		return &pb.PrimaryPatchFactorRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := validateRow(ctx, rows[0], s.validate); err != nil {
		return &pb.PrimaryPatchFactorRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	node, err := s.resolveInputCommitNode(ctx, rows[0].GetKey())
	if err != nil {
		return &pb.PrimaryPatchFactorRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.PrimaryPatchFactorRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := node.PatchFactor(ctx, &pb.PatchFactorReq{
		AuthInfo: auth, CommitId: req.GetCommitId(), BindingVersion: req.GetBindingVersion(), OwnedFields: req.GetOwnedFields(), Row: rows[0],
	})
	if err != nil {
		return &pb.PrimaryPatchFactorRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return &pb.PrimaryPatchFactorRsp{RetInfo: rsp.GetRetInfo()}, nil
	}
	return &pb.PrimaryPatchFactorRsp{RetInfo: retinfo.Success("success"), Receipt: rsp.GetReceipt()}, nil
}

func (s *Service) LookupWriteReceipt(ctx context.Context, req *pb.PrimaryLookupWriteReceiptReq) (*pb.PrimaryLookupWriteReceiptRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" || req.GetCommitId() == "" {
		return &pb.PrimaryLookupWriteReceiptRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and commit_id are required"))}, nil
	}
	if err := s.authorizeRequest(req.GetAuthInfo()); err != nil {
		return &pb.PrimaryLookupWriteReceiptRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	ctx = s.requestContext(ctx)
	node, err := s.resolve(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.PrimaryLookupWriteReceiptRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	commitNode, ok := node.(inputCommitDataNodeClient)
	if !ok {
		return &pb.PrimaryLookupWriteReceiptRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, errors.New("DataNode input commit RPC is unavailable"))}, nil
	}
	auth, err := s.signAuth(req.GetAuthInfo())
	if err != nil {
		return &pb.PrimaryLookupWriteReceiptRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := commitNode.LookupWriteReceipt(ctx, &pb.LookupWriteReceiptReq{AuthInfo: auth, CommitId: req.GetCommitId()})
	if err != nil {
		return &pb.PrimaryLookupWriteReceiptRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return &pb.PrimaryLookupWriteReceiptRsp{RetInfo: rsp.GetRetInfo()}, nil
	}
	return &pb.PrimaryLookupWriteReceiptRsp{RetInfo: retinfo.Success("success"), Receipt: rsp.GetReceipt()}, nil
}

func (s *Service) resolveInputCommitNode(ctx context.Context, key *pb.RowKey) (inputCommitDataNodeClient, error) {
	if key == nil {
		return nil, errors.New("row key is required")
	}
	node, err := s.resolve(ctx, key.GetSpaceId(), key.GetDatasetId())
	if err != nil {
		return nil, err
	}
	commitNode, ok := node.(inputCommitDataNodeClient)
	if !ok {
		return nil, errors.New("DataNode input commit RPC is unavailable")
	}
	return commitNode, nil
}

func isMergeAppID(appID string) bool {
	switch strings.ToLower(strings.TrimSpace(appID)) {
	case "merge", "moox-merge":
		return true
	default:
		return false
	}
}

func rejectUnauthorizedUpsertSemantics(rows []*pb.RowFieldUpsert, writeSource string) error {
	if privilegedWriteSource(writeSource) {
		return fmt.Errorf("write_source %q cannot grant merge or factor write privileges", writeSource)
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		for name := range row.GetAttributes() {
			if reservedWriteAttribute(name) {
				return fmt.Errorf("attribute %q can only be set by authorized commit or patch", name)
			}
		}
	}
	return nil
}

func privilegedWriteSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "merge", "moox-merge", "factor", "moox-factor", "moox-factor-engine", pebble.WriteKindInputCommit, pebble.WriteKindFactorPatch:
		return true
	default:
		return false
	}
}

func reservedWriteAttribute(name string) bool {
	switch name {
	case "moox.input_ready", "moox.commit_id", "moox.binding_version":
		return true
	default:
		return false
	}
}
