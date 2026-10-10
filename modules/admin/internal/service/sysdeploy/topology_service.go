package sysdeploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	adminsecurity "github.com/mooyang-code/moox/modules/admin/internal/security"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

func (s *ServiceImpl) topologyDAO() (*TopologyDAO, error) {
	return NewTopologyDAO(s.dao.db, s.adminNodeID)
}

func topologyRet(err error) *pb.RetInfo {
	switch {
	case err == nil:
		return retOK()
	case errors.Is(err, gorm.ErrRecordNotFound):
		return retErr(pb.ErrorCode_NOT_FOUND, "主机或部署不存在")
	case errors.Is(err, ErrInvalidTopology):
		return retErr(pb.ErrorCode_INVALID_PARAM, err.Error())
	default:
		return retErr(pb.ErrorCode_INNER_ERR, "主机部署读写失败")
	}
}

func topologyPage(page *pb.Page, total int) (start, end int, result *pb.PageResult) {
	number, size := int(page.GetPage()), int(page.GetSize())
	if number == 0 {
		number = 1
	}
	// Host descriptions can contain up to 4 KiB, including JSON escapes. Keep
	// a full page below the catalog's 4 MiB response limit.
	if size == 0 || size > 100 {
		size = 50
	}
	start = min((number-1)*size, total)
	end = min(start+size, total)
	return start, end, makePageResult(number, size, int64(total))
}

func (s *ServiceImpl) GetCatalog(context.Context, *pb.GetCatalogReq) (*pb.GetCatalogRsp, error) {
	raw := servicecatalog.EmbeddedYAML()
	sum := sha256.Sum256(raw)
	return &pb.GetCatalogRsp{RetInfo: retOK(), CatalogYaml: string(raw), Sha256: hex.EncodeToString(sum[:]), ControlHostId: s.adminNodeID}, nil
}

func (s *ServiceImpl) ListHosts(ctx context.Context, req *pb.ListDeploymentHostsReq) (*pb.ListDeploymentHostsRsp, error) {
	dao, err := s.topologyDAO()
	if err != nil {
		return &pb.ListDeploymentHostsRsp{RetInfo: topologyRet(err)}, nil
	}
	hosts, _, err := dao.Read(ctx)
	response := &pb.ListDeploymentHostsRsp{RetInfo: topologyRet(err)}
	if err != nil {
		return response, nil
	}
	hosts = slices.DeleteFunc(hosts, func(h HostRecord) bool {
		return req.GetHostId() != "" && h.HostID != req.GetHostId() || req.GetStatus() != "" && h.Status != req.GetStatus()
	})
	start, end, page := topologyPage(req.GetPage(), len(hosts))
	response.PageResult = page
	hosts = hosts[start:end]
	for _, h := range hosts {
		response.Hosts = append(response.Hosts, &pb.DeploymentHost{HostId: h.HostID, Address: h.Address, PrivateAddress: h.PrivateAddress, Region: h.Region, Status: h.Status, Description: h.Description, CreatedAt: h.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: h.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	}
	return response, nil
}

func (s *ServiceImpl) ListPlacements(ctx context.Context, req *pb.ListPlacementsReq) (*pb.ListPlacementsRsp, error) {
	dao, err := s.topologyDAO()
	if err != nil {
		return &pb.ListPlacementsRsp{RetInfo: topologyRet(err)}, nil
	}
	_, placements, err := dao.Read(ctx)
	response := &pb.ListPlacementsRsp{RetInfo: topologyRet(err)}
	if err != nil {
		return response, nil
	}
	placements = slices.DeleteFunc(placements, func(p PlacementRecord) bool {
		return req.GetHostId() != "" && p.HostID != req.GetHostId() || req.GetComponentId() != "" && p.ComponentID != req.GetComponentId() || req.GetStatus() != "" && p.Status != req.GetStatus()
	})
	start, end, page := topologyPage(req.GetPage(), len(placements))
	response.PageResult = page
	placements = placements[start:end]
	for _, p := range placements {
		response.Placements = append(response.Placements, &pb.ComponentPlacement{HostId: p.HostID, ComponentId: p.ComponentID, Status: p.Status, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: p.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	}
	return response, nil
}

func (s *ServiceImpl) GetDirectory(ctx context.Context, _ *pb.GetDirectoryReq) (*pb.GetDirectoryRsp, error) {
	dao, err := s.topologyDAO()
	if err != nil {
		return &pb.GetDirectoryRsp{RetInfo: topologyRet(err)}, nil
	}
	compiled, err := dao.Compile(ctx, dao.controlHostID)
	if err != nil {
		return &pb.GetDirectoryRsp{RetInfo: topologyRet(err)}, nil
	}
	return &pb.GetDirectoryRsp{RetInfo: retOK(), Directory: directoryToProto(compiled.Directory)}, nil
}

func (s *ServiceImpl) GetHostRoutes(ctx context.Context, req *pb.GetHostRoutesReq) (*pb.GetHostRoutesRsp, error) {
	if req.GetHostId() == "" {
		return &pb.GetHostRoutesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "host_id is required")}, nil
	}
	dao, err := s.topologyDAO()
	if err != nil {
		return &pb.GetHostRoutesRsp{RetInfo: topologyRet(err)}, nil
	}
	encryptionKey, err := adminsecurity.GetEncryptionKey()
	if err != nil {
		return &pb.GetHostRoutesRsp{RetInfo: topologyRet(err)}, nil
	}
	compiled, snapshot, err := dao.CompileSnapshot(ctx, req.GetHostId(), encryptionKey)
	compiledAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		return &pb.GetHostRoutesRsp{RetInfo: topologyRet(err)}, nil
	}
	status, err := dao.GetGatewayStatus(ctx, req.GetHostId())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status, err = &HostGatewayStatus{HostID: req.GetHostId()}, nil
	}
	if err != nil {
		return &pb.GetHostRoutesRsp{RetInfo: topologyRet(err)}, nil
	}
	// Always show the current desired snapshot, including rotations/withdrawals
	// since the latest heartbeat. The gateway cannot choose its expected hash.
	status.ExpectedHash = snapshot.Hash
	response := &pb.GetHostRoutesRsp{RetInfo: retOK(), HostId: compiled.HostID, DefinitionHash: compiled.Hash, GatewayStatus: statusToProto(status), SnapshotSchemaVersion: snapshot.SchemaVersion, CompiledAt: compiledAt}
	for _, route := range compiled.Routes {
		response.Routes = append(response.Routes, routeToProto(route))
	}
	return response, nil
}

func (s *ServiceImpl) SyncHostPlacements(ctx context.Context, req *pb.SyncHostPlacementsReq) (*pb.SyncHostPlacementsRsp, error) {
	dao, err := s.topologyDAO()
	if err == nil {
		err = dao.SyncHostPlacements(ctx, HostSpec{HostID: req.GetHostId(), Address: req.GetAddress(), PrivateAddress: req.GetPrivateAddress(), Region: req.GetRegion(), Description: req.GetDescription(), Components: req.GetComponentIds()})
	}
	return &pb.SyncHostPlacementsRsp{RetInfo: topologyRet(err)}, nil
}

func (s *ServiceImpl) SetHostStatus(ctx context.Context, req *pb.SetHostStatusReq) (*pb.SetHostStatusRsp, error) {
	if req.GetHostId() == "" {
		return &pb.SetHostStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "host_id is required")}, nil
	}
	dao, err := s.topologyDAO()
	if err == nil {
		err = dao.SetHostStatus(ctx, req.GetHostId(), req.GetStatus())
	}
	return &pb.SetHostStatusRsp{RetInfo: topologyRet(err)}, nil
}

func (s *ServiceImpl) SetPlacementStatus(ctx context.Context, req *pb.SetPlacementStatusReq) (*pb.SetPlacementStatusRsp, error) {
	if req.GetHostId() == "" || req.GetComponentId() == "" {
		return &pb.SetPlacementStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "host_id and component_id are required")}, nil
	}
	dao, err := s.topologyDAO()
	if err == nil {
		err = dao.SetPlacementStatus(ctx, req.GetHostId(), req.GetComponentId(), req.GetStatus())
	}
	return &pb.SetPlacementStatusRsp{RetInfo: topologyRet(err)}, nil
}

func (s *ServiceImpl) DeleteHost(ctx context.Context, req *pb.DeleteDeploymentHostReq) (*pb.DeleteDeploymentHostRsp, error) {
	if req.GetHostId() == "" {
		return &pb.DeleteDeploymentHostRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "host_id is required")}, nil
	}
	dao, err := s.topologyDAO()
	if err == nil {
		err = dao.DeleteHost(ctx, req.GetHostId())
	}
	return &pb.DeleteDeploymentHostRsp{RetInfo: topologyRet(err)}, nil
}

func directoryToProto(d servicecatalog.Directory) *directorypb.ServiceDirectory {
	result := &directorypb.ServiceDirectory{Version: d.Version, Hosts: map[string]*directorypb.DirectoryHost{}, Services: map[string]*directorypb.ServiceHosts{}}
	for id, host := range d.Hosts {
		result.Hosts[id] = &directorypb.DirectoryHost{Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region}
	}
	for path, hosts := range d.Services {
		result.Services[path] = &directorypb.ServiceHosts{HostIds: append([]string{}, hosts...)}
	}
	return result
}

func routeToProto(route servicecatalog.Route) *pb.HostGatewayRoute {
	return &pb.HostGatewayRoute{ComponentId: route.ComponentID, ServicePath: route.ServicePath, Method: route.Method, Address: route.Address, TimeoutMs: route.TimeoutMS, MaxBodyBytes: route.MaxBodyBytes, Callers: append([]string{}, route.Callers...), ReadOnly: route.ReadOnly}
}

func statusToProto(status *HostGatewayStatus) *pb.HostGatewayRuntimeStatus {
	format := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	return &pb.HostGatewayRuntimeStatus{InstanceId: status.InstanceID, Version: status.Version, ExpectedHash: status.ExpectedHash, AppliedHash: status.AppliedHash, RouteCount: status.RouteCount, LastSeenAt: format(status.LastSeenAt), LastError: status.LastError, PreviousInstanceId: status.PreviousInstanceID, ReplacedAt: format(status.ReplacedAt), ConflictInstanceId: status.ConflictInstanceID, ConflictSeenAt: format(status.ConflictSeenAt)}
}
