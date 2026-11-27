package sysdeploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	compiled, err := dao.Compile(ctx, req.GetHostId())
	if err != nil {
		return &pb.GetHostRoutesRsp{RetInfo: topologyRet(err)}, nil
	}
	snapshot, err := dao.Snapshot(ctx)
	compiledAt := time.Now().UnixMilli()
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
	// 期望哈希总是按当前登记的内容实时计算，包含上次心跳之后的变更；不冗余落库。
	response := &pb.GetHostRoutesRsp{RetInfo: retOK(), HostId: compiled.HostID, GatewayStatus: statusToProto(status, snapshot.Hash), SnapshotSchemaVersion: snapshot.SchemaVersion, CompiledAt: compiledAt}
	for _, route := range compiled.Routes {
		response.Routes = append(response.Routes, &pb.HostGatewayRoute{ComponentId: route.ComponentID, ServicePath: route.ServicePath, Method: route.Method, Address: route.Address, TimeoutMs: route.TimeoutMS, MaxBodyBytes: route.MaxBodyBytes, Callers: route.Callers, ReadOnly: route.ReadOnly})
	}
	return response, nil
}

// SyncTopology 按 moox.toml 的整份声明覆盖主机与部署的登记。
func (s *ServiceImpl) SyncTopology(ctx context.Context, req *pb.SyncTopologyReq) (*pb.SyncTopologyRsp, error) {
	specs := make([]HostSpec, 0, len(req.GetHosts()))
	for _, host := range req.GetHosts() {
		spec := HostSpec{HostID: host.GetHostId(), Enabled: host.GetEnabled(), Label: host.GetLabel(), Address: host.GetAddress(), PrivateAddress: host.GetPrivateAddress(), Region: host.GetRegion(),
			Reachability: host.GetReachability(), ExternalEntry: host.GetExternalEntry(), Description: host.GetDescription(), Components: host.GetComponentIds(), DisabledComponents: host.GetDisabledComponentIds()}
		if host.GetSshPort() != 0 {
			ssh := &SSHSpec{Port: int(host.GetSshPort()), User: host.GetSshUser()}
			switch host.GetSshAuthType() {
			case "password":
				ssh.Password = host.GetSshSecret()
			case "private_key":
				ssh.PrivateKey, ssh.Passphrase = host.GetSshSecret(), host.GetSshPassphrase()
			}
			spec.SSH = ssh
		}
		specs = append(specs, spec)
	}
	dao, err := s.topologyDAO()
	var touched []string
	if err == nil {
		touched, err = dao.ReplaceTopology(ctx, specs)
	}
	// 连接信息变了或主机被删掉：旧终端和文件会话不能继续连着旧凭据或旧地址。
	for _, hostID := range touched {
		s.closeHostSessions(hostID)
	}
	return &pb.SyncTopologyRsp{RetInfo: topologyRet(err)}, nil
}

func statusToProto(status *HostGatewayStatus, expectedHash string) *pb.HostGatewayRuntimeStatus {
	result := &pb.HostGatewayRuntimeStatus{ExpectedHash: expectedHash, AppliedHash: status.AppliedHash}
	if status.LastSeenAt != nil {
		result.LastSeenAt = status.LastSeenAt.UnixMilli()
	}
	if status.RelaySessions != "" {
		// 写入时已校验；读不出时宁可不展示，也不让整个状态查询失败。
		_ = json.Unmarshal([]byte(status.RelaySessions), &result.RelaySessions)
	}
	return result
}
