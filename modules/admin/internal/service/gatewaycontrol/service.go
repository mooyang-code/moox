// Package gatewaycontrol 给各主机的网关提供快照并记录它们的心跳。服务只听本机回环端口：别的主机的请求由控制机网关
// 验签后转进来，并在 x-moox-verified-host 头里带上验过的签名主机；控制机网关自己直连时也带这个头。这里不再验第二遍。
package gatewaycontrol

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var instancePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Service struct {
	db            *gorm.DB
	controlHostID string
	now           func() time.Time
}

func (*Service) String() string     { return "GatewayControl{per-host snapshots}" }
func (s *Service) GoString() string { return s.String() }

func NewService(db *gorm.DB, controlHostID string) (*Service, error) {
	if db == nil || !servicecatalog.ValidHostID(controlHostID) {
		return nil, errors.New("gateway control requires the Admin database and a canonical control host")
	}
	return &Service{db: db, controlHostID: controlHostID, now: time.Now}, nil
}

type verifiedHost struct{}

// sameHost 要求请求里的主机就是网关验过签的那台：一台主机只能取自己的快照、报自己的心跳。
func sameHost(ctx context.Context, hostID string) bool {
	host, _ := ctx.Value(verifiedHost{}).(string)
	return servicecatalog.ValidHostID(hostID) && host == hostID
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
	_, snapshot, err := dao.CompileSnapshot(ctx, req.GetHostId())
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
		len(req.GetError()) <= 4096 && utf8.ValidString(req.GetError()) && validRelaySessions(req.GetRelaySessions())
}

func validRelaySessions(sessions []*pb.RelaySessionStatus) bool {
	if len(sessions) > 1024 {
		return false
	}
	seen := map[string]bool{}
	for _, item := range sessions {
		if item == nil || !servicecatalog.ValidHostID(item.GetHostId()) || seen[item.GetHostId()] || item.GetReconnects() < 0 || item.GetStreams() < 0 ||
			len(item.GetInstanceId()) > 128 || !utf8.ValidString(item.GetInstanceId()) {
			return false
		}
		if at := item.GetConnectedAt(); at != "" {
			if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
				return false
			}
		}
		seen[item.GetHostId()] = true
	}
	return true
}

func (s *Service) ReportStatus(ctx context.Context, req *pb.ReportStatusReq) (*pb.ReportStatusRsp, error) {
	if !sameHost(ctx, req.GetHostId()) {
		return nil, errors.New("gateway control caller must match request host")
	}
	if !validReport(req) || len(req.GetRelaySessions()) != 0 && req.GetHostId() != s.controlHostID {
		return &pb.ReportStatusRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "无效的网关心跳"}}, nil
	}
	relaySessions := ""
	if sessions := req.GetRelaySessions(); len(sessions) != 0 {
		raw, err := json.Marshal(sessions)
		if err != nil {
			return &pb.ReportStatusRsp{RetInfo: result(err)}, nil
		}
		relaySessions = string(raw)
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
		_, snapshot, err := dao.CompileSnapshot(ctx, req.GetHostId())
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
		status.RelaySessions = relaySessions
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
