// Package gatewaycontrol 实现网关控制（trpc.moox.admin.GatewayControl）：向每台主机网关下发快照
// （本机路由、全局服务目录、校验密钥），并接收心跳。只允许 host-gateway@<同一主机> 调用。
package gatewaycontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/placement"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Service 实现 GatewayControl。
type Service struct {
	pb.UnimplementedGatewayControl
	placements *placement.Service
	keys       *keys.Service
	now        func() time.Time
}

// NewService 创建网关控制服务。
func NewService(placements *placement.Service, keys *keys.Service) *Service {
	return &Service{placements: placements, keys: keys, now: func() time.Time { return time.Now().UTC() }}
}

// Snapshot 是一台主机网关的完整快照，以及它的校验密钥范围。
type Snapshot struct {
	Proto   *pb.HostSnapshot
	Callers []string
}

// Build 按当前部署编译一台主机的快照。
func (s *Service) Build(ctx context.Context, hostID string) (Snapshot, error) {
	compiled, err := s.placements.Compile(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	config, ok := compiled.HostConfig(hostID)
	if !ok {
		return Snapshot{}, fmt.Errorf("%w: 主机 %s", placement.ErrNotFound, hostID)
	}
	routeSnapshot, err := gatewayroute.NormalizeAndHashState(hostID, config.Disabled, config.Routes)
	if err != nil {
		return Snapshot{}, fmt.Errorf("编译主机 %s 的路由: %w", hostID, err)
	}
	verification, err := s.keys.VerificationKeys(ctx, keys.CategoryCaller, config.Callers)
	if err != nil {
		return Snapshot{}, err
	}
	stateKeys := make([]gatewayroute.VerificationKey, 0, len(verification))
	protoKeys := make([]*pb.VerificationKey, 0, len(verification))
	for _, key := range verification {
		stateKeys = append(stateKeys, gatewayroute.VerificationKey{KeyID: key.KeyID, Caller: key.Caller, Secret: key.Secret})
		protoKeys = append(protoKeys, &pb.VerificationKey{KeyId: key.KeyID, Caller: key.Caller, Secret: key.Secret})
	}
	hash, err := gatewayroute.StateHash(routeSnapshot.RouteHash, compiled.Directory.Version, stateKeys)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := &pb.HostSnapshot{
		HostId: hostID, Hash: hash, GeneratedAt: s.now().Format(time.RFC3339Nano), Disabled: config.Disabled,
		Directory: gatewayclient.DirectoryToProto(compiled.Directory), Keys: protoKeys,
	}
	for _, route := range routeSnapshot.Routes {
		snapshot.Routes = append(snapshot.Routes, RouteToProto(route))
	}
	return Snapshot{Proto: snapshot, Callers: config.Callers}, nil
}

// RouteToProto 把路由转换为下发结构。
func RouteToProto(route gatewayroute.Route) *pb.HostRoute {
	return &pb.HostRoute{
		ComponentId: route.ServiceID, ServicePath: route.ServicePath, Address: route.Address,
		TimeoutMs: route.TimeoutMS, MaxBodyBytes: route.MaxBodyBytes,
		Methods: append([]string(nil), route.AllowedMethods...), Callers: append([]string(nil), route.AllowedCallers...),
	}
}

// PullSnapshot 按哈希返回变化：与主机网关当前的哈希相同时只返回 changed=false。
func (s *Service) PullSnapshot(ctx context.Context, req *pb.PullSnapshotReq) (*pb.PullSnapshotRsp, error) {
	hostID := strings.TrimSpace(req.GetHostId())
	if err := authorize(ctx, hostID); err != nil {
		return &pb.PullSnapshotRsp{RetInfo: retError(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	snapshot, err := s.Build(ctx, hostID)
	if err != nil {
		return &pb.PullSnapshotRsp{RetInfo: placementError(err)}, nil
	}
	if err := s.placements.SetExpectedHash(ctx, hostID, snapshot.Proto.GetHash()); err != nil {
		return &pb.PullSnapshotRsp{RetInfo: placementError(err)}, nil
	}
	if req.GetCurrentHash() == snapshot.Proto.GetHash() {
		return &pb.PullSnapshotRsp{RetInfo: retOK()}, nil
	}
	return &pb.PullSnapshotRsp{RetInfo: retOK(), Changed: true, Snapshot: snapshot.Proto}, nil
}

// ReportStatus 记录心跳，区分实例替换与冲突。
func (s *Service) ReportStatus(ctx context.Context, req *pb.ReportStatusReq) (*pb.ReportStatusRsp, error) {
	hostID := strings.TrimSpace(req.GetHostId())
	if err := authorize(ctx, hostID); err != nil {
		return &pb.ReportStatusRsp{RetInfo: retError(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	report := placement.GatewayReport{
		HostID: hostID, InstanceID: strings.TrimSpace(req.GetInstanceId()), Version: strings.TrimSpace(req.GetVersion()),
		AppliedHash: strings.TrimSpace(req.GetAppliedHash()), RouteCount: req.GetRouteCount(), LastError: req.GetLastError(),
	}
	if raw := strings.TrimSpace(req.GetCertificateNotAfter()); raw != "" {
		notAfter, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return &pb.ReportStatusRsp{RetInfo: retError(pb.ErrorCode_INVALID_PARAM, fmt.Errorf("证书到期时间 %q 不是 RFC3339: %w", raw, err))}, nil
		}
		report.CertificateNotAfter = &notAfter
	}
	_, err := s.placements.RecordGatewayReport(ctx, report)
	if err != nil {
		return &pb.ReportStatusRsp{RetInfo: placementError(err)}, nil
	}
	return &pb.ReportStatusRsp{RetInfo: retOK()}, nil
}

// authorize 只允许 host-gateway@<请求中的主机> 调用。调用方身份由主机网关在转发时写入；
// control 的主机网关直连本机端口时自己写入同一个元数据。
func authorize(ctx context.Context, hostID string) error {
	if hostID == "" {
		return errors.New("缺少主机 ID")
	}
	caller := strings.TrimSpace(string(trpc.GetMetaData(ctx, gatewayroute.MetadataVerifiedCaller)))
	if want := servicecatalog.HostGatewayIdentity(hostID); caller != want {
		return fmt.Errorf("调用方 %q 不能代表主机 %s，只允许 %s", caller, hostID, want)
	}
	return nil
}

func retOK() *pb.RetInfo { return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"} }

func retError(code pb.ErrorCode, err error) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: err.Error()}
}

// placementError 把部署相关的错误转换为返回码。
func placementError(err error) *pb.RetInfo {
	switch {
	case errors.Is(err, placement.ErrNotFound):
		return retError(pb.ErrorCode_NOT_FOUND, err)
	case errors.Is(err, placement.ErrInvalid):
		return retError(pb.ErrorCode_INVALID_PARAM, err)
	default:
		return retError(pb.ErrorCode_INNER_ERR, err)
	}
}
