// Package sysdeploy 实现 SysDeploy 服务：组件目录、主机与部署的查看和启用 / 停用，以及 CLI 按 moox.toml 同步部署记录。
// 组件定义写在代码里的组件目录（packages/servicecatalog），主机与部署的读写在 placement 包。
package sysdeploy

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/gatewaycontrol"
	"github.com/mooyang-code/moox/modules/admin/internal/service/placement"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// Service 是 SysDeploy 的 tRPC 实现。
type Service struct {
	pb.UnimplementedSysDeploy
	placements *placement.Service
	control    *gatewaycontrol.Service
}

// NewService 创建 SysDeploy 服务：部署记录委托 placement，主机路由快照委托网关控制。
func NewService(placements *placement.Service, control *gatewaycontrol.Service) *Service {
	return &Service{placements: placements, control: control}
}

// GetCatalog 返回组件目录，供服务部署页展示组件详情。
func (s *Service) GetCatalog(context.Context, *pb.GetCatalogReq) (*pb.GetCatalogRsp, error) {
	catalog := s.placements.Catalog()
	rsp := &pb.GetCatalogRsp{RetInfo: retOK(), Checksum: catalog.Checksum()}
	for _, component := range catalog.Components {
		item := &pb.CatalogComponent{
			Id: component.ID, Name: component.Name, Binary: component.Binary, Scope: string(component.Scope),
			Replicas: string(component.Replicas), Protected: component.Protected,
			HealthKind: string(component.Health.Kind), HealthPort: int32(component.Health.Port),
		}
		for _, port := range component.Ports {
			item.Ports = append(item.Ports, &pb.CatalogPort{Name: port.Name, Port: int32(port.Port)})
		}
		for _, service := range component.Services {
			out := &pb.CatalogService{
				Path: service.Path, Port: int32(service.Port), ConsoleName: service.ConsoleName,
				TimeoutMs: service.TimeoutMS, MaxBodyBytes: service.MaxBodyBytes,
			}
			for _, method := range service.RPCs {
				out.Methods = append(out.Methods, &pb.CatalogMethod{
					Name: method, ReadOnly: service.IsReadOnly(method), Callers: catalog.MethodCallers(service.Path, method),
				})
			}
			item.Services = append(item.Services, out)
		}
		rsp.Components = append(rsp.Components, item)
	}
	for _, principal := range catalog.Principals {
		item := &pb.CatalogPrincipal{Id: principal.ID, Description: principal.Description}
		for _, grant := range principal.Allow {
			item.Allow = append(item.Allow, &pb.CatalogPrincipalGrant{Service: grant.Service, Methods: append([]string(nil), grant.Methods...)})
		}
		rsp.Principals = append(rsp.Principals, item)
	}
	return rsp, nil
}

// ListHosts 返回全部主机与主机网关状态。
func (s *Service) ListHosts(ctx context.Context, _ *pb.ListDeployHostsReq) (*pb.ListDeployHostsRsp, error) {
	hosts, err := s.placements.ListHosts(ctx)
	if err != nil {
		return &pb.ListDeployHostsRsp{RetInfo: placementError(err)}, nil
	}
	statuses, err := s.placements.ListGatewayStatus(ctx)
	if err != nil {
		return &pb.ListDeployHostsRsp{RetInfo: placementError(err)}, nil
	}
	rsp := &pb.ListDeployHostsRsp{RetInfo: retOK()}
	now := time.Now()
	for _, host := range hosts {
		item := hostToProto(host)
		item.Gateway = gatewayStatusToProto(statuses[host.HostID], now)
		rsp.Hosts = append(rsp.Hosts, item)
	}
	return rsp, nil
}

// ListPlacements 按主机、组件筛选部署。
func (s *Service) ListPlacements(ctx context.Context, req *pb.ListPlacementsReq) (*pb.ListPlacementsRsp, error) {
	rows, err := s.placements.ListPlacements(ctx, strings.TrimSpace(req.GetHostId()), strings.TrimSpace(req.GetComponentId()))
	if err != nil {
		return &pb.ListPlacementsRsp{RetInfo: placementError(err)}, nil
	}
	rsp := &pb.ListPlacementsRsp{RetInfo: retOK()}
	for _, row := range rows {
		rsp.Placements = append(rsp.Placements, s.placementToProto(row))
	}
	return rsp, nil
}

// GetHostRoutes 返回一台主机编译后的路由、期望哈希、校验密钥范围和主机网关状态。
func (s *Service) GetHostRoutes(ctx context.Context, req *pb.GetHostRoutesReq) (*pb.GetHostRoutesRsp, error) {
	hostID := strings.TrimSpace(req.GetHostId())
	snapshot, err := s.control.Build(ctx, hostID)
	if err != nil {
		return &pb.GetHostRoutesRsp{RetInfo: placementError(err)}, nil
	}
	statuses, err := s.placements.ListGatewayStatus(ctx)
	if err != nil {
		return &pb.GetHostRoutesRsp{RetInfo: placementError(err)}, nil
	}
	return &pb.GetHostRoutesRsp{
		RetInfo: retOK(), HostId: hostID, Disabled: snapshot.Proto.GetDisabled(), ExpectedHash: snapshot.Proto.GetHash(),
		GeneratedAt: snapshot.Proto.GetGeneratedAt(), Routes: snapshot.Proto.GetRoutes(), Callers: snapshot.Callers,
		Gateway: gatewayStatusToProto(statuses[hostID], time.Now()),
	}, nil
}

// GetDirectory 返回当前的全局服务目录。
func (s *Service) GetDirectory(ctx context.Context, _ *pb.GetDirectoryReq) (*pb.GetDirectoryRsp, error) {
	compiled, err := s.placements.Compile(ctx)
	if err != nil {
		return &pb.GetDirectoryRsp{RetInfo: placementError(err)}, nil
	}
	return &pb.GetDirectoryRsp{RetInfo: retOK(), Directory: gatewayclient.DirectoryToProto(compiled.Directory)}, nil
}

// SetHostStatus 启用或停用整台主机；control 主机受保护。
func (s *Service) SetHostStatus(ctx context.Context, req *pb.SetHostStatusReq) (*pb.SetHostStatusRsp, error) {
	host, err := s.placements.SetHostStatus(ctx, strings.TrimSpace(req.GetHostId()), strings.TrimSpace(req.GetStatus()))
	if err != nil {
		return &pb.SetHostStatusRsp{RetInfo: placementError(err)}, nil
	}
	return &pb.SetHostStatusRsp{RetInfo: retOK(), Host: hostToProto(host)}, nil
}

// SetPlacementStatus 启用或停用一条部署；受保护的组件不能停用。停用只摘掉路由、停止健康检查，不停止进程。
func (s *Service) SetPlacementStatus(ctx context.Context, req *pb.SetPlacementStatusReq) (*pb.SetPlacementStatusRsp, error) {
	row, err := s.placements.SetPlacementStatus(ctx, strings.TrimSpace(req.GetHostId()), strings.TrimSpace(req.GetComponentId()), strings.TrimSpace(req.GetStatus()))
	if err != nil {
		return &pb.SetPlacementStatusRsp{RetInfo: placementError(err)}, nil
	}
	return &pb.SetPlacementStatusRsp{RetInfo: retOK(), Placement: s.placementToProto(row)}, nil
}

// SyncHostPlacements 按 moox.toml 事务性地同步一台主机的部署，供 CLI 部署时调用。
func (s *Service) SyncHostPlacements(ctx context.Context, req *pb.SyncHostPlacementsReq) (*pb.SyncHostPlacementsRsp, error) {
	host := req.GetHost()
	result, err := s.placements.SyncHostPlacements(ctx, placement.HostSpec{
		HostID: host.GetHostId(), Address: host.GetAddress(), PrivateAddress: host.GetPrivateAddress(),
		Region: host.GetRegion(), Description: host.GetDescription(),
	}, req.GetComponents())
	if err != nil {
		return &pb.SyncHostPlacementsRsp{RetInfo: placementError(err)}, nil
	}
	return &pb.SyncHostPlacementsRsp{
		RetInfo: retOK(), HostCreated: result.HostCreated, Added: result.Added, Removed: result.Removed, Kept: result.Kept,
	}, nil
}

// DeleteHost 删除一台主机；主机上仍有非主机范围的部署时拒绝。
func (s *Service) DeleteHost(ctx context.Context, req *pb.DeleteDeployHostReq) (*pb.DeleteDeployHostRsp, error) {
	if err := s.placements.DeleteHost(ctx, strings.TrimSpace(req.GetHostId())); err != nil {
		return &pb.DeleteDeployHostRsp{RetInfo: placementError(err)}, nil
	}
	return &pb.DeleteDeployHostRsp{RetInfo: retOK()}, nil
}

func hostToProto(host placement.Host) *pb.DeployHost {
	return &pb.DeployHost{
		HostId: host.HostID, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region,
		Status: host.Status, Description: host.Description, Protected: host.HostID == servicecatalog.ControlHostID,
		CreatedAt: formatTime(host.CreatedAt), UpdatedAt: formatTime(host.UpdatedAt),
	}
}

func (s *Service) placementToProto(row placement.Placement) *pb.DeployPlacement {
	item := &pb.DeployPlacement{
		HostId: row.HostID, ComponentId: row.ComponentID, Status: row.Status,
		CreatedAt: formatTime(row.CreatedAt), UpdatedAt: formatTime(row.UpdatedAt),
	}
	if component, ok := s.placements.Catalog().Component(row.ComponentID); ok {
		item.Protected = component.Protected
		item.HostComponent = component.Scope == servicecatalog.ScopeHost
	}
	return item
}

func gatewayStatusToProto(status placement.GatewayStatus, now time.Time) *pb.HostGatewayStatus {
	return &pb.HostGatewayStatus{
		State: status.State(now), Synced: status.Synced(), InstanceId: status.InstanceID, Version: status.Version,
		ExpectedHash: status.ExpectedHash, AppliedHash: status.AppliedHash, RouteCount: status.RouteCount,
		LastSeenAt: formatTimePtr(status.LastSeenAt), LastError: status.LastError,
		PreviousInstanceId: status.PreviousInstanceID, ReplacedAt: formatTimePtr(status.ReplacedAt),
		ConflictInstanceId: status.ConflictInstanceID, ConflictSeenAt: formatTimePtr(status.ConflictSeenAt),
		OutOfSyncSince: formatTimePtr(status.MismatchSince),
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatTimePtr(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatTime(*value)
}

func retOK() *pb.RetInfo { return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"} }

func placementError(err error) *pb.RetInfo {
	code := pb.ErrorCode_INNER_ERR
	switch {
	case errors.Is(err, placement.ErrNotFound):
		code = pb.ErrorCode_NOT_FOUND
	case errors.Is(err, placement.ErrInvalid):
		code = pb.ErrorCode_INVALID_PARAM
	}
	return &pb.RetInfo{Code: code, Msg: err.Error()}
}
