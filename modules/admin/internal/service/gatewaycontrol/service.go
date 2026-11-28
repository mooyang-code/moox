// Package gatewaycontrol 给各主机的网关提供快照；每次拉取兼作心跳，在进程内存里记下已应用快照、时间与反向连接会话（不落库）。
// 服务只听本机回环端口：别的主机的请求由控制机网关验签后转进来，并在 x-moox-verified-host 头里带上验过的签名主机；
// 控制机网关自己直连时也带这个头。这里不再验第二遍。
package gatewaycontrol

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

type Service struct {
	topology      *sysdeploy.TopologyDAO
	statuses      *sysdeploy.GatewayStatusStore
	controlHostID string
	now           func() time.Time
}

func (*Service) String() string     { return "GatewayControl{snapshots}" }
func (s *Service) GoString() string { return s.String() }

// Statuses 返回写入网关运行态的进程内存储（与 SysDeploy 共用）。
func (s *Service) Statuses() *sysdeploy.GatewayStatusStore { return s.statuses }

func NewService(db *gorm.DB, controlHostID string, statuses *sysdeploy.GatewayStatusStore) (*Service, error) {
	if db == nil || !servicecatalog.ValidHostID(controlHostID) {
		return nil, errors.New("gateway control requires the Admin database and a canonical control host")
	}
	topology, err := sysdeploy.NewTopologyDAO(db, controlHostID)
	if err != nil {
		return nil, err
	}
	if statuses == nil {
		statuses = sysdeploy.NewGatewayStatusStore()
	}
	return &Service{topology: topology, statuses: statuses, controlHostID: controlHostID, now: time.Now}, nil
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
	return &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "网关控制状态读取失败"}
}

// PullSnapshot 以只读事务读取快照，并在进程内存里记下这台网关的运行态：
// 每次拉取都更新（哈希没变也更新），因为它兼作心跳；没登记的主机拿不到快照，也不记入运行态。
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
	snapshot, err := s.topology.Snapshot(ctx)
	if err != nil {
		return &pb.PullSnapshotRsp{RetInfo: result(err)}, nil
	}
	// 快照对全部主机相同；没登记的主机拿不到，也不记运行态。
	if !slices.ContainsFunc(snapshot.Hosts, func(host *pb.SnapshotHost) bool { return host.HostId == req.GetHostId() }) {
		return &pb.PullSnapshotRsp{RetInfo: result(gorm.ErrRecordNotFound)}, nil
	}
	s.statuses.Record(req.GetHostId(), req.GetCurrentHash(), s.now(), req.GetRelaySessions())
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
			item.GetConnectedAt() < 0 || len(item.GetInstanceId()) > 128 || !utf8.ValidString(item.GetInstanceId()) {
			return false
		}
		seen[item.GetHostId()] = true
	}
	return true
}
