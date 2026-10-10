package publishlease

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/utils"
	"github.com/mooyang-code/moox/modules/admin/internal/service/space"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"gorm.io/gorm"
)

type Service struct {
	pb.UnimplementedCollectorPublishLease
	dao        *DAO
	authorizer space.Service
}

func NewService(db *gorm.DB, authorizer space.Service) *Service {
	return &Service{dao: NewDAO(db), authorizer: authorizer}
}

func (s *Service) AcquireCollectorPublishLease(ctx context.Context, req *pb.AcquireCollectorPublishLeaseReq) (*pb.CollectorPublishLeaseRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetHolderId()) == "" {
		return leaseRspError(pb.ErrorCode_INVALID_PARAM, "space_id and holder_id are required"), nil
	}
	if err := s.authorizeUser(ctx, req.GetSpaceId()); err != nil {
		return leaseRspError(pb.ErrorCode_NO_PERMISSION, "collector publish lease requires space owner or admin"), nil
	}
	var lease *leaseRecord
	var err error
	if req.GetExpectedFencingToken() > 0 {
		lease, err = s.dao.AcquireRecovery(ctx, req.GetSpaceId(), req.GetHolderId(), req.GetExpectedFencingToken(), time.Now().UTC())
	} else {
		lease, err = s.dao.Acquire(ctx, req.GetSpaceId(), req.GetHolderId(), time.Now().UTC())
	}
	if err != nil {
		if errors.Is(err, ErrLeaseHeld) {
			return leaseRspError(pb.ErrorCode_CONFLICT, ErrLeaseHeld.Error()), nil
		}
		if errors.Is(err, ErrLeaseSuperseded) {
			return leaseRspError(pb.ErrorCode_CONFLICT, ErrLeaseSuperseded.Error()), nil
		}
		return leaseRspError(pb.ErrorCode_INNER_ERR, "acquire collector publish lease failed"), nil
	}
	return leaseRsp(lease), nil
}

func (s *Service) RenewCollectorPublishLease(ctx context.Context, req *pb.RenewCollectorPublishLeaseReq) (*pb.CollectorPublishLeaseRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return leaseRspError(pb.ErrorCode_INVALID_PARAM, "space_id and lease identity are required"), nil
	}
	if err := s.authorizeUser(ctx, req.GetSpaceId()); err != nil {
		return leaseRspError(pb.ErrorCode_NO_PERMISSION, "collector publish lease requires space owner or admin"), nil
	}
	lease, err := s.dao.Renew(ctx, req.GetSpaceId(), req.GetLeaseId(), req.GetFencingToken(), time.Now().UTC())
	if err != nil {
		if errors.Is(err, ErrLeaseStale) {
			return leaseRspError(pb.ErrorCode_CONFLICT, ErrLeaseStale.Error()), nil
		}
		return leaseRspError(pb.ErrorCode_INNER_ERR, "renew collector publish lease failed"), nil
	}
	return leaseRsp(lease), nil
}

func (s *Service) ReleaseCollectorPublishLease(ctx context.Context, req *pb.ReleaseCollectorPublishLeaseReq) (*pb.ReleaseCollectorPublishLeaseRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ReleaseCollectorPublishLeaseRsp{RetInfo: leaseRet(pb.ErrorCode_INVALID_PARAM, "lease request is required")}, nil
	}
	if err := s.authorizeUser(ctx, req.GetSpaceId()); err != nil {
		return &pb.ReleaseCollectorPublishLeaseRsp{RetInfo: leaseRet(pb.ErrorCode_NO_PERMISSION, "collector publish lease requires space owner or admin")}, nil
	}
	released, err := s.dao.Release(ctx, req.GetSpaceId(), req.GetLeaseId(), req.GetFencingToken(), time.Now().UTC())
	if err != nil {
		return &pb.ReleaseCollectorPublishLeaseRsp{RetInfo: leaseRet(pb.ErrorCode_INNER_ERR, "release collector publish lease failed")}, nil
	}
	return &pb.ReleaseCollectorPublishLeaseRsp{RetInfo: leaseRet(pb.ErrorCode_SUCCESS, "ok"), Released: released}, nil
}

func (s *Service) ValidateCollectorPublishLease(ctx context.Context, req *pb.ValidateCollectorPublishLeaseReq) (*pb.ValidateCollectorPublishLeaseRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetLeaseId()) == "" || req.GetFencingToken() < 1 {
		return &pb.ValidateCollectorPublishLeaseRsp{RetInfo: leaseRet(pb.ErrorCode_INVALID_PARAM, "space_id, lease_id, and positive fencing_token are required")}, nil
	}
	if err := s.authorizeUser(ctx, req.GetSpaceId()); err != nil {
		return &pb.ValidateCollectorPublishLeaseRsp{RetInfo: leaseRet(pb.ErrorCode_NO_PERMISSION, "collector publish lease requires space owner or admin")}, nil
	}
	current, valid, err := s.dao.Validate(ctx, req.GetSpaceId(), req.GetLeaseId(), req.GetFencingToken(), time.Now().UTC())
	if err != nil {
		return &pb.ValidateCollectorPublishLeaseRsp{RetInfo: leaseRet(pb.ErrorCode_INNER_ERR, "validate collector publish lease failed")}, nil
	}
	response := &pb.ValidateCollectorPublishLeaseRsp{RetInfo: leaseRet(pb.ErrorCode_SUCCESS, "ok"), Valid: valid}
	if current != nil {
		response.CurrentFencingToken = current.FencingToken
		response.ExpiresAt = time.UnixMilli(current.ExpiresAtUnix).UTC().Format(time.RFC3339Nano)
	}
	return response, nil
}

func (s *Service) BeginCollectorPublishOperation(ctx context.Context, req *pb.BeginCollectorPublishOperationReq) (*pb.CollectorPublishOperationRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetLeaseId()) == "" || strings.TrimSpace(req.GetOperationId()) == "" || req.GetFencingToken() < 1 {
		return operationRspError(pb.ErrorCode_INVALID_PARAM, "space_id, lease_id, operation_id, and positive fencing_token are required"), nil
	}
	if err := s.authorizeUser(ctx, req.GetSpaceId()); err != nil {
		return operationRspError(pb.ErrorCode_NO_PERMISSION, "collector publish operation requires space owner or admin"), nil
	}
	operation, err := s.dao.BeginOperation(ctx, req.GetSpaceId(), req.GetLeaseId(), req.GetOperationId(), req.GetFencingToken(), time.Now().UTC())
	if err != nil {
		if errors.Is(err, ErrLeaseStale) {
			return operationRspError(pb.ErrorCode_CONFLICT, ErrLeaseStale.Error()), nil
		}
		return operationRspError(pb.ErrorCode_INNER_ERR, "begin collector publish operation failed"), nil
	}
	return operationRsp(operation), nil
}

func (s *Service) RenewCollectorPublishOperation(ctx context.Context, req *pb.CollectorPublishOperationReq) (*pb.CollectorPublishOperationRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetOperationId()) == "" || req.GetFencingToken() < 1 {
		return operationRspError(pb.ErrorCode_INVALID_PARAM, "space_id, operation_id, and positive fencing_token are required"), nil
	}
	if err := s.authorizeUser(ctx, req.GetSpaceId()); err != nil {
		return operationRspError(pb.ErrorCode_NO_PERMISSION, "collector publish operation requires space owner or admin"), nil
	}
	operation, err := s.dao.RenewOperation(ctx, req.GetSpaceId(), req.GetOperationId(), req.GetFencingToken(), time.Now().UTC())
	if err != nil {
		if errors.Is(err, ErrLeaseStale) {
			return operationRspError(pb.ErrorCode_CONFLICT, ErrLeaseStale.Error()), nil
		}
		return operationRspError(pb.ErrorCode_INNER_ERR, "renew collector publish operation failed"), nil
	}
	return operationRsp(operation), nil
}

func (s *Service) EndCollectorPublishOperation(ctx context.Context, req *pb.CollectorPublishOperationReq) (*pb.CollectorPublishOperationRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetOperationId()) == "" || req.GetFencingToken() < 1 {
		return operationRspError(pb.ErrorCode_INVALID_PARAM, "space_id, operation_id, and positive fencing_token are required"), nil
	}
	if err := s.authorizeUser(ctx, req.GetSpaceId()); err != nil {
		return operationRspError(pb.ErrorCode_NO_PERMISSION, "collector publish operation requires space owner or admin"), nil
	}
	_, err := s.dao.EndOperation(ctx, req.GetSpaceId(), req.GetOperationId(), req.GetFencingToken())
	if err != nil {
		return operationRspError(pb.ErrorCode_INNER_ERR, "end collector publish operation failed"), nil
	}
	return &pb.CollectorPublishOperationRsp{RetInfo: leaseRet(pb.ErrorCode_SUCCESS, "ok"), Active: false}, nil
}

func (s *Service) authorizeUser(ctx context.Context, spaceID string) error {
	userID, _, role, err := utils.GetUserInfoFromCtx(ctx)
	if err != nil {
		// 没有用户身份的是服务调用方，主机网关已按组件目录的 ACL 校验过（只有 moox-cli、console、cloudnode、
		// collector）；浏览器请求经过会话鉴权，在下面按空间成员关系校验。
		return nil
	}
	if s.authorizer == nil {
		return fmt.Errorf("space authorizer is unavailable")
	}
	return s.authorizer.AuthorizeTradeRequest(ctx, userID, strings.TrimSpace(spaceID), "CollectorPublishLease", role)
}

func leaseRsp(record *leaseRecord) *pb.CollectorPublishLeaseRsp {
	if record == nil {
		return leaseRspError(pb.ErrorCode_INNER_ERR, "lease result is missing")
	}
	return &pb.CollectorPublishLeaseRsp{
		RetInfo: leaseRet(pb.ErrorCode_SUCCESS, "ok"), SpaceId: record.SpaceID,
		LeaseId: record.LeaseID, FencingToken: record.FencingToken,
		ExpiresAt: time.UnixMilli(record.ExpiresAtUnix).UTC().Format(time.RFC3339Nano),
	}
}

func leaseRspError(code pb.ErrorCode, message string) *pb.CollectorPublishLeaseRsp {
	return &pb.CollectorPublishLeaseRsp{RetInfo: leaseRet(code, message)}
}

func leaseRet(code pb.ErrorCode, message string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: message}
}

func operationRsp(record *operationRecord) *pb.CollectorPublishOperationRsp {
	if record == nil {
		return operationRspError(pb.ErrorCode_INNER_ERR, "operation result is missing")
	}
	return &pb.CollectorPublishOperationRsp{
		RetInfo: leaseRet(pb.ErrorCode_SUCCESS, "ok"), Active: true,
		ExpiresAt: time.UnixMilli(record.ExpiresAtUnix).UTC().Format(time.RFC3339Nano),
	}
}

func operationRspError(code pb.ErrorCode, message string) *pb.CollectorPublishOperationRsp {
	return &pb.CollectorPublishOperationRsp{RetInfo: leaseRet(code, message)}
}
