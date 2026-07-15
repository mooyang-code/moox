package sysdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Service 管理系统服务部署信息，并向 cloudnode keepalive 提供 SCF runtime payload。
type Service interface {
	pb.SysDeployService
	SeedDefaults(ctx context.Context) error
	GetServiceDeployments(ctx context.Context) (map[string]interface{}, error)
	ResolveAdminServiceDetail(ctx context.Context, adminNodeID, serviceID string) (gateway.ServiceDetail, bool)
	CompileGatewaySnapshot(ctx context.Context, nodeID string) (gatewayproxy.Snapshot, error)
	ReportGatewayStatus(ctx context.Context, report GatewayStatusReport) error
}

func (s *ServiceImpl) CompileGatewaySnapshot(ctx context.Context, nodeID string) (gatewayproxy.Snapshot, error) {
	return s.dao.CompileGatewaySnapshot(ctx, nodeID)
}

func (s *ServiceImpl) ReportGatewayStatus(ctx context.Context, report GatewayStatusReport) error {
	return s.dao.ReportGatewayStatus(ctx, report)
}

type ServiceImpl struct {
	pb.UnimplementedSysDeploy
	dao *DAO
}

func NewService(dbManager *database.Manager) *ServiceImpl {
	return &ServiceImpl{dao: NewDAO(dbManager.GetDB())}
}

func (s *ServiceImpl) SeedDefaults(ctx context.Context) error {
	nodeID := localNodeID()
	node, err := s.dao.GetGatewayNode(ctx, nodeID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		node := &GatewayNode{NodeID: nodeID, Name: nodeID, PublicAddress: "https://" + defaultPublicHost, Status: "enabled"}
		if err := s.dao.CreateGatewayNode(ctx, node); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	node, err = s.dao.GetGatewayNode(ctx, nodeID)
	if err != nil {
		return err
	}
	if node.HostID == nil {
		publicURL, _ := url.Parse(node.PublicAddress)
		var hostID int64
		if publicURL != nil && publicURL.Hostname() != "" {
			if err := s.dao.db.WithContext(ctx).Table("t_ssh_host").Select("c_id").Where("c_address = ?", publicURL.Hostname()).Limit(1).Scan(&hostID).Error; err != nil {
				return err
			}
		}
		if hostID > 0 {
			if err := s.dao.db.WithContext(ctx).Model(&GatewayNode{}).Where("c_node_id = ?", nodeID).Update("c_host_id", hostID).Error; err != nil {
				return err
			}
		}
	}
	return s.dao.SeedDefaults(ctx, DefaultDeployments(nodeID))
}

func localNodeID() string {
	if value := strings.TrimSpace(os.Getenv("MOOX_ADMIN_NODE_ID")); value != "" {
		return value
	}
	return "gateway-gz-122"
}

func (s *ServiceImpl) ListServiceDeployments(ctx context.Context, req *pb.ListServiceDeploymentsReq) (*pb.ListServiceDeploymentsRsp, error) {
	pageNo, offset, limit := normalizePage(req.GetPage())
	rows, total, err := s.dao.List(ctx, ListFilter{
		NodeID:         req.GetNodeId(),
		ServiceName:    req.GetServiceName(),
		ServiceKind:    req.GetServiceKind(),
		Scope:          req.GetScope(),
		Status:         req.GetStatus(),
		GatewayEnabled: req.GatewayEnabled,
	}, offset, limit)
	if err != nil {
		log.ErrorContextf(ctx, "[SysDeploy] ListServiceDeployments failed: %v", err)
		return &pb.ListServiceDeploymentsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询服务部署信息失败")}, nil
	}
	return &pb.ListServiceDeploymentsRsp{
		RetInfo:     retOK(),
		Deployments: modelsToPB(rows),
		PageResult:  makePageResult(pageNo, limit, total),
		Warnings:    storageTopologyWarnings(""),
	}, nil
}

func (s *ServiceImpl) GetServiceDeployment(ctx context.Context, req *pb.GetServiceDeploymentReq) (*pb.GetServiceDeploymentRsp, error) {
	if req.GetNodeId() == "" || req.GetServiceName() == "" {
		return &pb.GetServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id and service_name are required")}, nil
	}
	row, err := s.dao.Get(ctx, req.GetNodeId(), req.GetServiceName())
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &pb.GetServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "服务部署信息不存在")}, nil
		}
		log.ErrorContextf(ctx, "[SysDeploy] GetServiceDeployment failed: %v", err)
		return &pb.GetServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询服务部署信息失败")}, nil
	}
	return &pb.GetServiceDeploymentRsp{RetInfo: retOK(), Deployment: modelToPB(row), Warnings: storageTopologyWarnings(row.ServiceName)}, nil
}

func (s *ServiceImpl) CreateServiceDeployment(ctx context.Context, req *pb.CreateServiceDeploymentReq) (*pb.CreateServiceDeploymentRsp, error) {
	item := pbToModel(req.GetDeployment())
	if err := validateDeployment(item); err != nil {
		return &pb.CreateServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if _, err := s.dao.GetGatewayNode(ctx, item.NodeID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &pb.CreateServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "gateway node not found")}, nil
		}
		return &pb.CreateServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "query gateway node failed")}, nil
	}
	if err := s.dao.Create(ctx, item); err != nil {
		log.ErrorContextf(ctx, "[SysDeploy] CreateServiceDeployment failed: %v", err)
		code := pb.ErrorCode_INNER_ERR
		if strings.Contains(err.Error(), "already exists") {
			code = pb.ErrorCode_INVALID_PARAM
		}
		return &pb.CreateServiceDeploymentRsp{RetInfo: retErr(code, err.Error())}, nil
	}
	return &pb.CreateServiceDeploymentRsp{RetInfo: retOK(), Deployment: modelToPB(item), Warnings: storageTopologyWarnings(item.ServiceName)}, nil
}

func (s *ServiceImpl) UpdateServiceDeployment(ctx context.Context, req *pb.UpdateServiceDeploymentReq) (*pb.UpdateServiceDeploymentRsp, error) {
	item := pbToModel(req.GetDeployment())
	serviceName := req.GetServiceName()
	nodeID := req.GetNodeId()
	if serviceName == "" && item != nil {
		serviceName = item.ServiceName
	}
	if item != nil {
		item.ServiceName, item.NodeID = serviceName, nodeID
	}
	if err := validateDeployment(item); err != nil {
		return &pb.UpdateServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.dao.Update(ctx, nodeID, serviceName, item); err != nil {
		log.ErrorContextf(ctx, "[SysDeploy] UpdateServiceDeployment failed: %v", err)
		code := pb.ErrorCode_INNER_ERR
		if errors.Is(err, gorm.ErrRecordNotFound) {
			code = pb.ErrorCode_NOT_FOUND
		}
		if isUniqueConstraintError(err) {
			code = pb.ErrorCode_INVALID_PARAM
		}
		return &pb.UpdateServiceDeploymentRsp{RetInfo: retErr(code, err.Error())}, nil
	}
	row, err := s.dao.Get(ctx, nodeID, serviceName)
	if err != nil {
		return &pb.UpdateServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "保存后读取失败")}, nil
	}
	return &pb.UpdateServiceDeploymentRsp{RetInfo: retOK(), Deployment: modelToPB(row), Warnings: storageTopologyWarnings(serviceName)}, nil
}

func (s *ServiceImpl) DeleteServiceDeployment(ctx context.Context, req *pb.DeleteServiceDeploymentReq) (*pb.DeleteServiceDeploymentRsp, error) {
	serviceName := req.GetServiceName()
	nodeID := req.GetNodeId()
	if serviceName == "" {
		return &pb.DeleteServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "service_name is required")}, nil
	}
	if nodeID == "" {
		return &pb.DeleteServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	if err := s.dao.Delete(ctx, nodeID, serviceName); err != nil {
		log.ErrorContextf(ctx, "[SysDeploy] DeleteServiceDeployment failed: %v", err)
		code := pb.ErrorCode_INNER_ERR
		if errors.Is(err, gorm.ErrRecordNotFound) {
			code = pb.ErrorCode_NOT_FOUND
		}
		return &pb.DeleteServiceDeploymentRsp{RetInfo: retErr(code, err.Error())}, nil
	}
	return &pb.DeleteServiceDeploymentRsp{RetInfo: retOK(), Warnings: storageTopologyWarnings(serviceName)}, nil
}

func (s *ServiceImpl) ListActiveServiceDeployments(ctx context.Context, req *pb.ListActiveServiceDeploymentsReq) (*pb.ListActiveServiceDeploymentsRsp, error) {
	rows, err := s.dao.ListActive(ctx, req.GetNodeId())
	if err != nil {
		log.ErrorContextf(ctx, "[SysDeploy] ListActiveServiceDeployments failed: %v", err)
		return &pb.ListActiveServiceDeploymentsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询 active 服务部署信息失败")}, nil
	}
	return &pb.ListActiveServiceDeploymentsRsp{RetInfo: retOK(), Deployments: modelsToPB(rows), DeploymentMap: endpointMap(rows, req.GetNodeId() == "")}, nil
}

// GetServiceDeployments 返回可直接序列化到 SCF keepalive event 的 active 部署信息。
func (s *ServiceImpl) GetServiceDeployments(ctx context.Context) (map[string]interface{}, error) {
	rows, err := s.dao.ListActive(ctx, localNodeID())
	if err != nil {
		return nil, err
	}
	payload := make(map[string]interface{}, len(rows))
	for i := range rows {
		row := rows[i]
		payload[row.ServiceName] = map[string]interface{}{
			"service_name": row.ServiceName,
			"service_kind": row.ServiceKind,
			"protocol":     row.Protocol,
			"host":         row.Host,
			"port":         row.Port,
			"base_url":     deploymentBaseURL(&row),
			"rpc_address":  deploymentRPCAddress(&row),
			"gateway_path": row.GatewayPath,
			"scope":        row.Scope,
			"status":       row.Status,
		}
	}
	return payload, nil
}

// ResolveAdminServiceDetail resolves browser control-plane forwarding only from
// active deployments assigned to the Admin process's configured node.
func (s *ServiceImpl) ResolveAdminServiceDetail(ctx context.Context, adminNodeID, serviceID string) (gateway.ServiceDetail, bool) {
	adminNodeID = strings.TrimSpace(adminNodeID)
	if adminNodeID == "" {
		return gateway.ServiceDetail{}, false
	}
	row, err := s.dao.Get(ctx, adminNodeID, gatewayDeploymentName(serviceID))
	if err != nil || row == nil || row.Status != "active" {
		return gateway.ServiceDetail{}, false
	}
	address := deploymentRPCAddress(row)
	path := strings.TrimSpace(row.GatewayPath)
	if address == "" || path == "" || strings.HasPrefix(path, "/") {
		return gateway.ServiceDetail{}, false
	}
	return gateway.ServiceDetail{Address: address, Path: path}, true
}

func gatewayDeploymentName(serviceID string) string {
	switch serviceID {
	case "auth":
		return "admin_auth"
	case "collector", "collectmgr":
		return "moox_collector"
	case "cloudnode":
		return "moox_cloudnode"
	case "factor", "factormgr":
		return "moox_factor"
	case "strategy", "strategymgr":
		return "moox_strategy"
	case "monitor":
		return "moox_monitor"
	case "archive":
		return "moox_archive"
	case "hostagent", "host-agent":
		return "moox_hostagent"
	case "trade":
		return "moox_trade"
	default:
		return serviceID
	}
}

func validateDeployment(item *Deployment) error {
	if item == nil {
		return fmt.Errorf("deployment is required")
	}
	normalizeDeployment(item)
	if item.NodeID == "" {
		return fmt.Errorf("node_id is required")
	}
	if item.ServiceName == "" {
		return fmt.Errorf("service_name is required")
	}
	if item.Host == "" {
		return fmt.Errorf("host is required")
	}
	ip := net.ParseIP(item.Host)
	if ip == nil {
		return fmt.Errorf("host must be an IP address")
	}
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("host must be a routable unicast IP address")
	}
	if item.Port <= 0 || item.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	switch item.Protocol {
	case "http", "https", "trpc":
	default:
		return fmt.Errorf("protocol must be http, https, or trpc")
	}
	if item.Scope != "public" && item.Scope != "internal" {
		return fmt.Errorf("scope must be public or internal")
	}
	if item.Status != "active" && item.Status != "disabled" {
		return fmt.Errorf("status must be active or disabled")
	}
	if strings.HasPrefix(item.GatewayPath, "/") && item.ServiceKind != "gateway" {
		return fmt.Errorf("gateway_path must be a tRPC service path for non-gateway deployments")
	}
	if item.ServiceKind == "gateway" && item.GatewayPath != "" && !strings.HasPrefix(item.GatewayPath, "/") {
		return fmt.Errorf("gateway gateway_path must start with /")
	}
	extra := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(item.ExtraConfig), &extra); err != nil || extra == nil {
		return fmt.Errorf("extra_config must be a valid JSON object")
	}
	if item.GatewayEnabled {
		if item.Protocol != "http" {
			return fmt.Errorf("gateway-enabled protocol must be http")
		}
		extra, err := parseRouteExtraConfig(item.ExtraConfig)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidGatewayRoute, err)
		}
		if requiresMethodAllowlist(item.GatewayServiceID, item.GatewayPath) && len(extra.GatewayMethods) == 0 {
			return fmt.Errorf("%w: %s requires nonempty gateway_methods", ErrInvalidGatewayRoute, item.GatewayServiceID)
		}
		route := gatewayproxy.Route{ServiceID: item.GatewayServiceID, Address: deploymentRPCAddress(item), ServicePath: item.GatewayPath, AllowedMethods: extra.GatewayMethods}
		if _, err := gatewayproxy.NormalizeAndHash("validation", []gatewayproxy.Route{route}); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidGatewayRoute, err)
		}
	}
	return nil
}

func normalizePage(page *pb.Page) (int, int, int) {
	pageNo, size := int(page.GetPage()), int(page.GetSize())
	if pageNo <= 0 {
		pageNo = 1
	}
	if size <= 0 || size > 500 {
		size = 50
	}
	return pageNo, (pageNo - 1) * size, size
}

func makePageResult(pageNo, size int, total int64) *pb.PageResult {
	return &pb.PageResult{Page: uint32(pageNo), Size: uint32(size), Total: uint32(total), HasMore: int64(pageNo*size) < total}
}

func retOK() *pb.RetInfo { return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"} }

func retErr(code pb.ErrorCode, msg string) *pb.RetInfo { return &pb.RetInfo{Code: code, Msg: msg} }

func modelsToPB(rows []Deployment) []*pb.ServiceDeployment {
	items := make([]*pb.ServiceDeployment, 0, len(rows))
	for i := range rows {
		items = append(items, modelToPB(&rows[i]))
	}
	return items
}

func modelToPB(row *Deployment) *pb.ServiceDeployment {
	if row == nil {
		return nil
	}
	return &pb.ServiceDeployment{
		Id:          row.ID,
		ServiceName: row.ServiceName,
		ServiceKind: row.ServiceKind,
		Protocol:    row.Protocol,
		Host:        row.Host,
		Port:        row.Port,
		BaseUrl:     deploymentBaseURL(row),
		RpcAddress:  deploymentRPCAddress(row),
		GatewayPath: row.GatewayPath,
		Scope:       row.Scope,
		Status:      row.Status,
		Description: row.Description,
		ExtraConfig: row.ExtraConfig,
		CreatedAt:   formatTime(row.CreatedAt),
		UpdatedAt:   formatTime(row.UpdatedAt),
		NodeId:      row.NodeID, GatewayServiceId: row.GatewayServiceID, GatewayEnabled: row.GatewayEnabled,
	}
}

func pbToModel(item *pb.ServiceDeployment) *Deployment {
	if item == nil {
		return nil
	}
	return &Deployment{
		ID:          item.GetId(),
		ServiceName: item.GetServiceName(),
		ServiceKind: item.GetServiceKind(),
		Protocol:    item.GetProtocol(),
		Host:        item.GetHost(),
		Port:        item.GetPort(),
		GatewayPath: item.GetGatewayPath(),
		Scope:       item.GetScope(),
		Status:      item.GetStatus(),
		Description: item.GetDescription(),
		ExtraConfig: item.GetExtraConfig(),
		NodeID:      item.GetNodeId(), GatewayServiceID: item.GetGatewayServiceId(), GatewayEnabled: item.GetGatewayEnabled(),
	}
}

func endpointMap(rows []Deployment, composite bool) map[string]*pb.ServiceDeploymentEndpoint {
	items := make(map[string]*pb.ServiceDeploymentEndpoint, len(rows))
	for i := range rows {
		row := rows[i]
		key := row.ServiceName
		if composite {
			key = row.NodeID + "/" + row.ServiceName
		}
		items[key] = &pb.ServiceDeploymentEndpoint{
			ServiceName: row.ServiceName,
			ServiceKind: row.ServiceKind,
			Protocol:    row.Protocol,
			Host:        row.Host,
			Port:        row.Port,
			BaseUrl:     deploymentBaseURL(&row),
			RpcAddress:  deploymentRPCAddress(&row),
			GatewayPath: row.GatewayPath,
			Scope:       row.Scope,
			Status:      row.Status,
			NodeId:      row.NodeID, GatewayServiceId: row.GatewayServiceID, GatewayEnabled: row.GatewayEnabled,
		}
	}
	return items
}

func deploymentRPCAddress(row *Deployment) string {
	if row == nil || row.Host == "" || row.Port <= 0 {
		return ""
	}
	return net.JoinHostPort(row.Host, strconv.Itoa(int(row.Port)))
}

func deploymentBaseURL(row *Deployment) string {
	if row == nil || (row.Protocol != "http" && row.Protocol != "https") {
		return ""
	}
	return row.Protocol + "://" + deploymentRPCAddress(row)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatTime(*t)
}
