// Package gatewaycontrol 给各主机的网关提供快照；每次拉取兼作心跳，记下期望快照、已应用快照与时间。服务只听本机回环端口：别的主机的请求由控制机网关
// 验签后转进来，并在 x-moox-verified-host 头里带上验过的签名主机；控制机网关自己直连时也带这个头。这里不再验第二遍。
package gatewaycontrol

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct {
	db            *gorm.DB
	controlHostID string
	now           func() time.Time
}

func (*Service) String() string     { return "GatewayControl{snapshots}" }
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

// PullSnapshot 返回快照，并在同一事务里记下这台网关的状态：期望哈希与快照读自同一时刻。
// 每次拉取都写（哈希没变也写），因为它兼作心跳；没登记的主机拿不到快照，也不写状态（表上有外键）。
func (s *Service) PullSnapshot(ctx context.Context, req *pb.PullSnapshotReq) (*pb.PullSnapshotRsp, error) {
	if !sameHost(ctx, req.GetHostId()) {
		return nil, errors.New("gateway control caller must match request host")
	}
	if req.GetCurrentHash() != "" && !validHash(req.GetCurrentHash()) {
		return &pb.PullSnapshotRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "current_hash 必须为 SHA-256"}}, nil
	}
	if !validRelaySessions(req.GetRelaySessions()) || len(req.GetRelaySessions()) != 0 && req.GetHostId() != s.controlHostID {
		return &pb.PullSnapshotRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "无效的反向连接会话"}}, nil
	}
	relaySessions := ""
	if sessions := req.GetRelaySessions(); len(sessions) != 0 {
		raw, err := json.Marshal(sessions)
		if err != nil {
			return &pb.PullSnapshotRsp{RetInfo: result(err)}, nil
		}
		relaySessions = string(raw)
	}
	var snapshot *pb.HostGatewaySnapshot
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先取写锁，与拓扑、密钥的写入串行：期望哈希与状态描述的是同一份库内状态。
		if err := tx.Exec("UPDATE t_host_gateway_status SET c_host_id = c_host_id WHERE 0").Error; err != nil {
			return err
		}
		dao, err := sysdeploy.NewTopologyDAO(tx, s.controlHostID)
		if err != nil {
			return err
		}
		if snapshot, err = dao.Snapshot(ctx); err != nil {
			return err
		}
		// 快照对全部主机相同；没登记的主机拿不到。
		if !slices.ContainsFunc(snapshot.Hosts, func(host *pb.SnapshotHost) bool { return host.HostId == req.GetHostId() }) {
			return gorm.ErrRecordNotFound
		}
		now := s.now().UTC()
		status := sysdeploy.HostGatewayStatus{HostID: req.GetHostId(), ExpectedHash: snapshot.Hash, AppliedHash: req.GetCurrentHash(), LastSeenAt: &now, RelaySessions: relaySessions}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_host_id"}}, UpdateAll: true}).Create(&status).Error
	})
	if err != nil {
		return &pb.PullSnapshotRsp{RetInfo: result(err)}, nil
	}
	if snapshot.Hash == req.GetCurrentHash() {
		return &pb.PullSnapshotRsp{RetInfo: success()}, nil
	}
	return &pb.PullSnapshotRsp{RetInfo: success(), Changed: true, Snapshot: snapshot}, nil
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
