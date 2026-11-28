package sysdeploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

func (s *ServiceImpl) topologyDAO() (*TopologyDAO, error) {
	if s.topology == nil {
		return nil, errors.New("topology DAO is not initialized")
	}
	return s.topology, nil
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
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return &pb.GetCatalogRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "加载组件目录失败")}, nil
	}
	return &pb.GetCatalogRsp{
		RetInfo:       retOK(),
		CatalogYaml:   string(raw),
		Sha256:        hex.EncodeToString(sum[:]),
		ControlHostId: s.adminNodeID,
		Catalog:       catalogToProto(catalog),
	}, nil
}

func catalogToProto(catalog servicecatalog.Catalog) *pb.ComponentCatalog {
	out := &pb.ComponentCatalog{
		Version:    int32(catalog.Version),
		Identities: make([]*pb.CatalogIdentity, 0, len(catalog.Identities)),
		Components: make([]*pb.CatalogComponent, 0, len(catalog.Components)),
	}
	for _, id := range catalog.Identities {
		out.Identities = append(out.Identities, &pb.CatalogIdentity{Id: id.ID, External: id.External})
	}
	for _, component := range catalog.Components {
		item := &pb.CatalogComponent{
			Id:              component.ID,
			Name:            component.Name,
			Binary:          component.Binary,
			Scope:           component.Scope,
			ExternalProgram: component.ExternalProgram,
			HealthPort:      int32(component.Health.Port),
		}
		for _, port := range component.Ports {
			item.Ports = append(item.Ports, int32(port))
		}
		for _, service := range component.Services {
			svc := &pb.CatalogService{
				Path:         service.Path,
				Port:         int32(service.Port),
				ConsoleName:  service.ConsoleName,
				TimeoutMs:    service.TimeoutFor(""),
				MaxBodyBytes: service.BodyLimit(),
			}
			for _, method := range service.Methods {
				svc.Methods = append(svc.Methods, &pb.CatalogMethod{
					Name:       method.Name,
					ReadOnly:   method.ReadOnly,
					Idempotent: method.Idempotent,
					TimeoutMs:  service.TimeoutFor(method.Name),
					Callers:    catalog.MethodCallers(method),
				})
			}
			item.Services = append(item.Services, svc)
		}
		out.Components = append(out.Components, item)
	}
	return out
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
		response.Hosts = append(response.Hosts, &pb.DeploymentHost{HostId: h.HostID, Label: h.Label, SshPort: int32(h.SSHPort), SshUser: h.SSHUser, Address: h.Address, PrivateAddress: h.PrivateAddress, Region: h.Region, Status: h.Status, Reachability: servicecatalog.ReachabilityName(h.Reachability), ExternalEntry: h.ExternalEntry, Description: h.Description, CreatedAt: h.CreatedAt.UnixMilli(), UpdatedAt: h.UpdatedAt.UnixMilli()})
	}
	return response, nil
}

func (s *ServiceImpl) ListDeployments(ctx context.Context, req *pb.ListDeploymentsReq) (*pb.ListDeploymentsRsp, error) {
	dao, err := s.topologyDAO()
	if err != nil {
		return &pb.ListDeploymentsRsp{RetInfo: topologyRet(err)}, nil
	}
	_, deployments, err := dao.Read(ctx)
	response := &pb.ListDeploymentsRsp{RetInfo: topologyRet(err)}
	if err != nil {
		return response, nil
	}
	deployments = slices.DeleteFunc(deployments, func(p DeploymentRecord) bool {
		return req.GetHostId() != "" && p.HostID != req.GetHostId() || req.GetComponentId() != "" && p.ComponentID != req.GetComponentId() || req.GetStatus() != "" && p.Status != req.GetStatus()
	})
	start, end, page := topologyPage(req.GetPage(), len(deployments))
	response.PageResult = page
	deployments = deployments[start:end]
	for _, p := range deployments {
		response.Deployments = append(response.Deployments, &pb.ComponentDeployment{HostId: p.HostID, ComponentId: p.ComponentID, Status: p.Status, CreatedAt: p.CreatedAt.UnixMilli(), UpdatedAt: p.UpdatedAt.UnixMilli()})
	}
	return response, nil
}

func (s *ServiceImpl) GetHostRoutes(ctx context.Context, req *pb.GetHostRoutesReq) (*pb.GetHostRoutesRsp, error) {
	if req.GetHostId() == "" {
		return &pb.GetHostRoutesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "host_id is required")}, nil
	}
	dao, err := s.topologyDAO()
	if err != nil {
		return &pb.GetHostRoutesRsp{RetInfo: topologyRet(err)}, nil
	}
	// 路由不随快照下发：这里和网关一样按登记的部署与服务目录算一遍，只给人看。
	// Compile 先核对主机仍在登记表里，已删主机留在内存中的运行态不会被返回。
	compiled, err := dao.Compile(ctx, req.GetHostId())
	if err != nil {
		return &pb.GetHostRoutesRsp{RetInfo: topologyRet(err)}, nil
	}
	snapshot, err := dao.Snapshot(ctx)
	compiledAt := time.Now().UnixMilli()
	if err != nil {
		return &pb.GetHostRoutesRsp{RetInfo: topologyRet(err)}, nil
	}
	status, _ := s.statuses.Get(req.GetHostId())
	// 期望哈希总是按当前登记的内容实时计算，包含上次心跳之后的变更；运行态从进程内存读。
	response := &pb.GetHostRoutesRsp{RetInfo: retOK(), HostId: compiled.HostID, GatewayStatus: statusToProto(status, snapshot.Hash), SnapshotSchemaVersion: snapshot.SchemaVersion, CompiledAt: compiledAt}
	for _, route := range compiled.Routes {
		response.Routes = append(response.Routes, &pb.HostGatewayRoute{ComponentId: route.ComponentID, ServicePath: route.ServicePath, Method: route.Method, Address: route.Address, TimeoutMs: route.TimeoutMS, MaxBodyBytes: route.MaxBodyBytes, Callers: route.Callers, ReadOnly: route.ReadOnly})
	}
	return response, nil
}

// SyncTopology 按 moox.toml 的整份声明覆盖主机与部署的登记。
func (s *ServiceImpl) SyncTopology(ctx context.Context, req *pb.SyncTopologyReq) (*pb.SyncTopologyRsp, error) {
	dao, err := s.topologyDAO()
	var touched []string
	if err == nil {
		touched, err = dao.ReplaceTopology(ctx, req)
	}
	// 连接信息变了或主机被删掉：旧终端和文件会话不能继续连着旧凭据或旧地址。
	for _, hostID := range touched {
		s.closeHostSessions(hostID)
	}
	return &pb.SyncTopologyRsp{RetInfo: topologyRet(err)}, nil
}

func statusToProto(status HostGatewayStatus, expectedHash string) *pb.HostGatewayRuntimeStatus {
	result := &pb.HostGatewayRuntimeStatus{
		ExpectedHash:  expectedHash,
		AppliedHash:   status.AppliedHash,
		RelaySessions: status.RelaySessions,
	}
	if status.LastSeenAt != nil {
		result.LastSeenAt = status.LastSeenAt.UnixMilli()
	}
	return result
}
