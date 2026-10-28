package datanode

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type Options struct {
	NodeID     string
	Store      *pebble.Store
	Root       string
	Pebble     pebble.Options
	AuthSecret string
}

type Service struct {
	nodeID      string
	store       *pebble.Store
	requireAuth bool
	authSecret  string
}

func ServiceAuthKey(secret, appID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(appID))
	return hex.EncodeToString(mac.Sum(nil))
}

var _ pb.DataNodeRuntimeService = (*Service)(nil)
var _ pb.DataNodePeriodRuntimeService = (*Service)(nil)
var _ pb.DataNodeDatasetAdminRuntimeService = (*Service)(nil)
var _ pb.DataNodeRangeRuntimeService = (*Service)(nil)

func NewService(opts Options) (*Service, error) {
	store := opts.Store
	if store == nil {
		pebbleOptions := opts.Pebble
		if pebbleOptions.NodeID == "" {
			pebbleOptions.NodeID = opts.NodeID
		}
		if pebbleOptions.Path == "" {
			pebbleOptions.Path = opts.Root
		}
		opened, err := pebble.Open(pebbleOptions)
		if err != nil {
			return nil, err
		}
		store = opened
	}
	nodeID := strings.TrimSpace(opts.NodeID)
	if nodeID == "" {
		nodeID = store.NodeID()
	}
	if nodeID == "" {
		_ = store.Close()
		return nil, errors.New("node_id is required")
	}
	secret := opts.AuthSecret
	if secret == "" {
		_ = store.Close()
		return nil, errors.New("auth secret is required")
	}
	return &Service{nodeID: nodeID, store: store, requireAuth: true, authSecret: secret}, nil
}

func (s *Service) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *Service) UpsertFields(ctx context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	if req == nil || len(req.GetRows()) == 0 {
		return &pb.UpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("rows are required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.UpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.UpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if _, err := s.store.UpsertFields(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetCommitId(), req.GetRows()); err != nil {
		return &pb.UpsertFieldsRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	// 写入已持久化，事件由节点的 outbox 转发协程异步发布。重复提交命中时同样返回全部行键。
	keys := make([]*pb.RowKey, 0, len(req.GetRows()))
	for _, row := range req.GetRows() {
		key, err := pebble.NormalizeRowKey(row.GetKey())
		if err != nil {
			return &pb.UpsertFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		keys = append(keys, key)
	}
	return &pb.UpsertFieldsRsp{RetInfo: retinfo.Success("success"), Keys: keys}, nil
}

func (s *Service) ReadRows(ctx context.Context, req *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 {
		return &pb.ReadRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("keys are required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.ReadRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.ReadRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if req.GetDatasetId() != "" {
		for _, key := range req.GetKeys() {
			if key.GetDatasetId() != req.GetDatasetId() {
				return &pb.ReadRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset_id does not match row key"))}, nil
			}
		}
	}
	rows, err := s.store.ReadRows(ctx, req.GetKeys(), req.GetColumnNames(), req.GetAttributeKeys())
	if err != nil {
		return &pb.ReadRowsRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.ReadRowsRsp{RetInfo: retinfo.Success("success"), Rows: rows}, nil
}

// ReadTimeSeriesRows 读取时序事实（时间区间或每个序列最近 N 根），由 PrimaryStore 对所有调用方转发到这里。
func (s *Service) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req == nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rows, err := s.store.ReadTimeSeriesRows(ctx, req)
	if err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	rows.RetInfo = retinfo.Success("success")
	return rows, nil
}

// CountTimeSeriesRows 按标的统计时间范围内已存在的时间点数，只在序列索引上扫描键。
func (s *Service) CountTimeSeriesRows(ctx context.Context, req *pb.CountTimeSeriesRowsReq) (*pb.CountTimeSeriesRowsRsp, error) {
	if req == nil {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	counts, err := s.store.CountTimeSeriesRows(ctx, req)
	if err != nil {
		return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.CountTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Counts: counts}, nil
}

// ReadDatasetChanges 从持久化的行变更日志里按序读取变更，由 PrimaryStore 转发。
func (s *Service) ReadDatasetChanges(ctx context.Context, req *pb.ReadDatasetChangesReq) (*pb.ReadDatasetChangesRsp, error) {
	if req == nil {
		return &pb.ReadDatasetChangesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.ReadDatasetChangesRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rsp, err := s.store.ReadDatasetChanges(ctx, req)
	if err != nil {
		return &pb.ReadDatasetChangesRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	rsp.RetInfo = retinfo.Success("success")
	return rsp, nil
}

func (s *Service) GetNodeState(ctx context.Context, req *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req == nil || req.GetNodeId() == "" {
		return &pb.GetNodeStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id is required"))}, nil
	}
	if req.GetNodeId() != s.nodeID {
		return &pb.GetNodeStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.GetNodeStateRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	return &pb.GetNodeStateRsp{RetInfo: retinfo.Success("success"), NodeId: s.nodeID, Status: "READY"}, nil
}

func (s *Service) CleanupExpiredBuckets(ctx context.Context, req *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" || req.GetBeforeBucketStart() == "" {
		return &pb.CleanupExpiredBucketsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id, dataset_id and before_bucket_start are required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.CleanupExpiredBucketsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.CleanupExpiredBucketsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	before, err := time.Parse(time.RFC3339Nano, req.GetBeforeBucketStart())
	if err != nil {
		return &pb.CleanupExpiredBucketsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	deleted, err := s.store.CleanupExpiredBuckets(ctx, req.GetSpaceId(), req.GetDatasetId(), before)
	if err != nil {
		return &pb.CleanupExpiredBucketsRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.CleanupExpiredBucketsRsp{RetInfo: retinfo.Success("success"), DeletedRanges: deleted}, nil
}

func (s *Service) DeleteDatasetRows(ctx context.Context, req *pb.DeleteDatasetRowsReq) (*pb.DeleteDatasetRowsRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.DeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.DeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.DeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	deleted, err := s.store.DeleteDatasetRows(ctx, req.GetSpaceId(), req.GetDatasetId())
	if err != nil {
		return &pb.DeleteDatasetRowsRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.DeleteDatasetRowsRsp{RetInfo: retinfo.Success("success"), DeletedRanges: deleted}, nil
}

func (s *Service) RestoreDatasetRows(ctx context.Context, req *pb.RestoreDatasetRowsReq) (*pb.RestoreDatasetRowsRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.RestoreDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.RestoreDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.RestoreDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if err := s.store.RestoreDatasetRows(ctx, req.GetSpaceId(), req.GetDatasetId()); err != nil {
		return &pb.RestoreDatasetRowsRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.RestoreDatasetRowsRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) validateAuth(auth *pb.AuthInfo) error {
	if !s.requireAuth {
		return nil
	}
	if auth == nil || strings.TrimSpace(auth.GetAppId()) == "" || strings.TrimSpace(auth.GetAppKey()) == "" {
		return errors.New("service auth is required")
	}
	expected := ServiceAuthKey(s.authSecret, auth.GetAppId())
	if !hmac.Equal([]byte(strings.ToLower(auth.GetAppKey())), []byte(expected)) {
		return errors.New("invalid service HMAC")
	}
	return nil
}

func errorCode(err error) pb.ErrorCode {
	if err == nil {
		return pb.ErrorCode_SUCCESS
	}
	if errors.Is(err, cpebble.ErrNotFound) {
		return pb.ErrorCode_NOT_FOUND
	}
	var validation pebble.ValidationError
	if errors.As(err, &validation) {
		return pb.ErrorCode_INVALID_PARAM
	}
	var conflict pebble.ConflictError
	if errors.As(err, &conflict) {
		return pb.ErrorCode_CONFLICT
	}
	var commitConflict pebble.CommitConflictError
	if errors.As(err, &commitConflict) {
		return pb.ErrorCode_CONFLICT
	}
	var periodConflict pebble.PeriodConflictError
	if errors.As(err, &periodConflict) {
		return pb.ErrorCode_CONFLICT
	}
	var staleEffectiveFrom pebble.StaleEffectiveFromError
	if errors.As(err, &staleEffectiveFrom) {
		return pb.ErrorCode_CONFLICT
	}
	var hashMismatch pebble.PeriodHashMismatchError
	if errors.As(err, &hashMismatch) {
		return pb.ErrorCode_CONFLICT
	}
	var snapshotMissing pebble.SeriesSnapshotMissingError
	if errors.As(err, &snapshotMissing) {
		return pb.ErrorCode_NOT_FOUND
	}
	var deadlinePassed pebble.PeriodDeadlinePassedError
	if errors.As(err, &deadlinePassed) {
		return pb.ErrorCode_INVALID_PARAM
	}
	return pb.ErrorCode_INNER_ERR
}

func (s *Service) Ready() bool {
	return s != nil && s.store != nil
}

func (s *Service) Store() *pebble.Store {
	if s == nil {
		return nil
	}
	return s.store
}
