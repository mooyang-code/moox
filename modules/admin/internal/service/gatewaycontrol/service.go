// Package gatewaycontrol serves authenticated per-host configuration and
// persists runtime heartbeats. Admin startup never seeds topology or keys here.
package gatewaycontrol

import (
	"context"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var instancePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type NonceConsumer interface {
	ConsumeGatewayControlNonce(context.Context, string, string, time.Duration) (bool, error)
}

type Service struct {
	db            *gorm.DB
	controlHostID string
	master        string
	keys          *keys.Store
	nonces        NonceConsumer
	now           func() time.Time
}

func (*Service) String() string     { return "GatewayControl{per-host authenticated snapshots}" }
func (s *Service) GoString() string { return s.String() }

func NewService(db *gorm.DB, controlHostID, encryptionKey string, nonces NonceConsumer) (*Service, error) {
	if !servicecatalog.ValidHostID(controlHostID) || nonces == nil {
		return nil, errors.New("gateway control requires canonical control host and durable nonce store")
	}
	store, err := keys.NewStore(db, encryptionKey)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, controlHostID: controlHostID, master: encryptionKey, keys: store, nonces: nonces, now: time.Now}, nil
}

type verifiedCaller struct{}

func sameHost(ctx context.Context, hostID string) bool {
	caller, _ := ctx.Value(verifiedCaller{}).(string)
	return servicecatalog.ValidHostID(hostID) && caller == "host-gateway@"+hostID
}

func validHash(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && value == strings.ToLower(value)
}

func success() *pb.RetInfo { return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS} }

func result(err error) *pb.RetInfo {
	if err == nil {
		return success()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &pb.RetInfo{Code: pb.ErrorCode_NOT_FOUND, Msg: "主机不存在"}
	}
	return &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "网关控制状态读写失败"}
}

func (s *Service) PullSnapshot(ctx context.Context, req *pb.PullSnapshotReq) (*pb.PullSnapshotRsp, error) {
	if !sameHost(ctx, req.GetHostId()) {
		return nil, errors.New("gateway control caller must match request host")
	}
	if req.GetCurrentHash() != "" && !validHash(req.GetCurrentHash()) {
		return &pb.PullSnapshotRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "current_hash 必须为 SHA-256"}}, nil
	}
	dao, err := sysdeploy.NewTopologyDAO(s.db, s.controlHostID)
	if err != nil {
		return &pb.PullSnapshotRsp{RetInfo: result(err)}, nil
	}
	_, snapshot, err := dao.CompileSnapshot(ctx, req.GetHostId(), s.master)
	if err != nil {
		return &pb.PullSnapshotRsp{RetInfo: result(err)}, nil
	}
	if snapshot.Hash == req.GetCurrentHash() {
		return &pb.PullSnapshotRsp{RetInfo: success()}, nil
	}
	return &pb.PullSnapshotRsp{RetInfo: success(), Changed: true, Snapshot: snapshot}, nil
}

func validReport(req *pb.ReportStatusReq) bool {
	return instancePattern.MatchString(req.GetInstanceId()) &&
		req.GetVersion() != "" && len(req.GetVersion()) <= 128 && utf8.ValidString(req.GetVersion()) &&
		req.GetVersion() == strings.TrimSpace(req.GetVersion()) && !strings.ContainsFunc(req.GetVersion(), unicode.IsControl) &&
		(req.GetAppliedHash() == "" || validHash(req.GetAppliedHash())) &&
		req.GetRouteCount() >= 0 && req.GetRouteCount() <= 10000 &&
		len(req.GetError()) <= 4096 && utf8.ValidString(req.GetError())
}

func (s *Service) ReportStatus(ctx context.Context, req *pb.ReportStatusReq) (*pb.ReportStatusRsp, error) {
	if !sameHost(ctx, req.GetHostId()) {
		return nil, errors.New("gateway control caller must match request host")
	}
	if !validReport(req) {
		return &pb.ReportStatusRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "无效的网关心跳"}}, nil
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize read/modify/write with all live/offline topology and key
		// writers. ExpectedHash and status describe the same database state.
		if err := tx.Exec("UPDATE t_host_gateway_status SET c_host_id = c_host_id WHERE 0").Error; err != nil {
			return err
		}
		dao, err := sysdeploy.NewTopologyDAO(tx, s.controlHostID)
		if err != nil {
			return err
		}
		_, snapshot, err := dao.CompileSnapshot(ctx, req.GetHostId(), s.master)
		if err != nil {
			return err
		}
		status := sysdeploy.HostGatewayStatus{HostID: req.GetHostId()}
		err = tx.Where("c_host_id = ?", req.GetHostId()).First(&status).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := s.now().UTC()
		updateInstance(&status, req.GetInstanceId(), now)
		status.Version, status.ExpectedHash, status.AppliedHash = req.GetVersion(), snapshot.Hash, req.GetAppliedHash()
		status.RouteCount, status.LastError, status.LastSeenAt = int64(req.GetRouteCount()), req.GetError(), &now
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_host_id"}}, UpdateAll: true}).Create(&status).Error
	})
	return &pb.ReportStatusRsp{RetInfo: result(err)}, nil
}

func updateInstance(status *sysdeploy.HostGatewayStatus, instance string, now time.Time) {
	status.ClearExpiredConflict(now)
	if status.InstanceID == "" {
		status.InstanceID = instance
		return
	}
	if status.InstanceID == instance {
		return
	}
	if status.PreviousInstanceID == instance && status.ReplacedAt != nil && now.Before(status.ReplacedAt.Add(sysdeploy.GatewayConflictWindow)) {
		status.ConflictInstanceID, status.ConflictSeenAt = status.InstanceID, &now
	}
	status.PreviousInstanceID, status.ReplacedAt, status.InstanceID = status.InstanceID, &now, instance
}
