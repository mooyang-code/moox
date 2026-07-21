package datanode

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

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

var _ pb.DataNodeService = (*Service)(nil)

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

func (s *Service) WriteFields(ctx context.Context, req *pb.WriteFieldsReq) (*pb.WriteFieldsRsp, error) {
	if req == nil || len(req.GetRows()) == 0 {
		return &pb.WriteFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("rows are required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.WriteFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.WriteFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	entries, err := s.store.WriteFieldsEvent(ctx, req.GetRows(), func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetFieldsChangedMessage(s.nodeID, spaceID, datasetID, rows)
	})
	if err != nil {
		return &pb.WriteFieldsRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	keys := make([]*pb.RowKey, 0, len(req.GetRows()))
	for _, row := range req.GetRows() {
		key, err := pebble.NormalizeRowKey(row.GetKey())
		if err != nil {
			return &pb.WriteFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		keys = append(keys, key)
	}
	_ = entries // entries are durable and relayed asynchronously by the node.
	return &pb.WriteFieldsRsp{RetInfo: retinfo.Success("success"), Keys: keys}, nil
}

func (s *Service) ReadFields(ctx context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 {
		return &pb.ReadFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("keys are required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.ReadFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.ReadFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	if req.GetDatasetId() != "" {
		for _, key := range req.GetKeys() {
			if key.GetDatasetId() != req.GetDatasetId() {
				return &pb.ReadFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset_id does not match row key"))}, nil
			}
		}
	}
	rows, existing, err := s.store.ReadFieldsWithPresence(ctx, req.GetKeys(), req.GetFieldIds(), req.GetAttributeKeys())
	if err != nil {
		return &pb.ReadFieldsRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.ReadFieldsRsp{RetInfo: retinfo.Success("success"), Rows: rows, ExistingKeys: existing}, nil
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
	return &pb.CleanupExpiredBucketsRsp{RetInfo: retinfo.Success("success"), DeletedBuckets: deleted}, nil
}

func (s *Service) ScanFields(ctx context.Context, req *pb.ScanFieldsReq) (*pb.ScanFieldsRsp, error) {
	if req == nil || req.GetSpaceId() == "" || req.GetDatasetId() == "" {
		return &pb.ScanFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("space_id and dataset_id are required"))}, nil
	}
	if req.GetNodeId() != "" && req.GetNodeId() != s.nodeID {
		return &pb.ScanFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("node_id does not match DataNode"))}, nil
	}
	if err := s.validateAuth(req.GetAuthInfo()); err != nil {
		return &pb.ScanFieldsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rows, page, nextToken, err := s.store.ScanFields(ctx, req.GetSpaceId(), req.GetDatasetId(), req.GetDataKind(), req.GetTimeRange(), req.GetVersionRange(), req.GetFieldIds(), req.GetAttributeKeys(), req.GetPage(), req.GetPageToken(), req.GetOrder())
	if err != nil {
		return &pb.ScanFieldsRsp{RetInfo: retinfo.Error(errorCode(err), err)}, nil
	}
	return &pb.ScanFieldsRsp{RetInfo: retinfo.Success("success"), Rows: rows, PageResult: page, NextPageToken: nextToken}, nil
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
	var validation pebble.ValidationError
	if errors.As(err, &validation) {
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
