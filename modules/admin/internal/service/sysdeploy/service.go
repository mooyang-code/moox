package sysdeploy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/gateway"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Service 管理系统服务部署信息，并向 cloudnode keepalive 提供 SCF runtime payload。
type Service interface {
	pb.SysDeployService
	SeedDefaults(ctx context.Context) error
	GetServiceDeployments(ctx context.Context) (map[string]interface{}, error)
	ResolveGatewayServiceDetail(ctx context.Context, serviceID string) (gateway.ServiceDetail, bool)
}

type ServiceImpl struct {
	pb.UnimplementedSysDeploy
	dao *DAO
}

func NewService(dbManager *database.Manager) *ServiceImpl {
	return &ServiceImpl{dao: NewDAO(dbManager.GetDB())}
}

func (s *ServiceImpl) SeedDefaults(ctx context.Context) error {
	return s.dao.SeedDefaults(ctx, DefaultDeployments())
}

func (s *ServiceImpl) ListServiceDeployments(ctx context.Context, req *pb.ListServiceDeploymentsReq) (*pb.ListServiceDeploymentsRsp, error) {
	pageNo, offset, limit := normalizePage(req.GetPage())
	rows, total, err := s.dao.List(ctx, ListFilter{
		ServiceName: req.GetServiceName(),
		ServiceKind: req.GetServiceKind(),
		Scope:       req.GetScope(),
		Status:      req.GetStatus(),
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
	row, err := s.dao.Get(ctx, req.GetServiceName())
	if err != nil {
		if err == gorm.ErrRecordNotFound {
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
	if err := s.dao.Create(ctx, item); err != nil {
		log.ErrorContextf(ctx, "[SysDeploy] CreateServiceDeployment failed: %v", err)
		return &pb.CreateServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.CreateServiceDeploymentRsp{RetInfo: retOK(), Deployment: modelToPB(item), Warnings: storageTopologyWarnings(item.ServiceName)}, nil
}

func (s *ServiceImpl) UpdateServiceDeployment(ctx context.Context, req *pb.UpdateServiceDeploymentReq) (*pb.UpdateServiceDeploymentRsp, error) {
	item := pbToModel(req.GetDeployment())
	serviceName := req.GetServiceName()
	if serviceName == "" && item != nil {
		serviceName = item.ServiceName
	}
	if item != nil {
		item.ServiceName = serviceName
	}
	if err := validateDeployment(item); err != nil {
		return &pb.UpdateServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.dao.Update(ctx, serviceName, item); err != nil {
		log.ErrorContextf(ctx, "[SysDeploy] UpdateServiceDeployment failed: %v", err)
		return &pb.UpdateServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	row, err := s.dao.Get(ctx, serviceName)
	if err != nil {
		return &pb.UpdateServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "保存后读取失败")}, nil
	}
	return &pb.UpdateServiceDeploymentRsp{RetInfo: retOK(), Deployment: modelToPB(row), Warnings: storageTopologyWarnings(serviceName)}, nil
}

func (s *ServiceImpl) DeleteServiceDeployment(ctx context.Context, req *pb.DeleteServiceDeploymentReq) (*pb.DeleteServiceDeploymentRsp, error) {
	serviceName := req.GetServiceName()
	if serviceName == "" {
		return &pb.DeleteServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "service_name is required")}, nil
	}
	if err := s.dao.Delete(ctx, serviceName); err != nil {
		log.ErrorContextf(ctx, "[SysDeploy] DeleteServiceDeployment failed: %v", err)
		return &pb.DeleteServiceDeploymentRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.DeleteServiceDeploymentRsp{RetInfo: retOK(), Warnings: storageTopologyWarnings(serviceName)}, nil
}

func (s *ServiceImpl) ListActiveServiceDeployments(ctx context.Context, req *pb.ListActiveServiceDeploymentsReq) (*pb.ListActiveServiceDeploymentsRsp, error) {
	rows, err := s.dao.ListActive(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "[SysDeploy] ListActiveServiceDeployments failed: %v", err)
		return &pb.ListActiveServiceDeploymentsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询 active 服务部署信息失败")}, nil
	}
	return &pb.ListActiveServiceDeploymentsRsp{RetInfo: retOK(), Deployments: modelsToPB(rows), DeploymentMap: endpointMap(rows)}, nil
}

// GetServiceDeployments 返回可直接序列化到 SCF keepalive event 的 active 部署信息。
func (s *ServiceImpl) GetServiceDeployments(ctx context.Context) (map[string]interface{}, error) {
	rows, err := s.dao.ListActive(ctx)
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
			"base_url":     row.BaseURL,
			"rpc_address":  row.RPCAddress,
			"gateway_path": row.GatewayPath,
			"scope":        row.Scope,
			"status":       row.Status,
		}
	}
	return payload, nil
}

// ResolveGatewayServiceDetail resolves /api/admin and /api/service forwarding
// targets from t_service_deployments. gateway.yaml remains a bootstrap fallback.
func (s *ServiceImpl) ResolveGatewayServiceDetail(ctx context.Context, serviceID string) (gateway.ServiceDetail, bool) {
	row, err := s.dao.Get(ctx, gatewayDeploymentName(serviceID))
	if err != nil || row == nil || row.Status != "active" {
		return gateway.ServiceDetail{}, false
	}
	address := row.RPCAddress
	if address == "" && row.Host != "" && row.Port > 0 {
		address = fmt.Sprintf("%s:%d", row.Host, row.Port)
	}
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
	case "collector":
		return "collector_api"
	default:
		return serviceID
	}
}

func validateDeployment(item *Deployment) error {
	if item == nil {
		return fmt.Errorf("deployment is required")
	}
	normalizeDeployment(item)
	if item.ServiceName == "" {
		return fmt.Errorf("service_name is required")
	}
	if item.Host == "" {
		return fmt.Errorf("host is required")
	}
	if item.Port <= 0 {
		return fmt.Errorf("port must be positive")
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
		BaseUrl:     row.BaseURL,
		RpcAddress:  row.RPCAddress,
		GatewayPath: row.GatewayPath,
		Scope:       row.Scope,
		Status:      row.Status,
		Description: row.Description,
		ExtraConfig: row.ExtraConfig,
		CreatedAt:   formatTime(row.CreatedAt),
		UpdatedAt:   formatTime(row.UpdatedAt),
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
		BaseURL:     item.GetBaseUrl(),
		RPCAddress:  item.GetRpcAddress(),
		GatewayPath: item.GetGatewayPath(),
		Scope:       item.GetScope(),
		Status:      item.GetStatus(),
		Description: item.GetDescription(),
		ExtraConfig: item.GetExtraConfig(),
	}
}

func endpointMap(rows []Deployment) map[string]*pb.ServiceDeploymentEndpoint {
	items := make(map[string]*pb.ServiceDeploymentEndpoint, len(rows))
	for i := range rows {
		row := rows[i]
		items[row.ServiceName] = &pb.ServiceDeploymentEndpoint{
			ServiceName: row.ServiceName,
			ServiceKind: row.ServiceKind,
			Protocol:    row.Protocol,
			Host:        row.Host,
			Port:        row.Port,
			BaseUrl:     row.BaseURL,
			RpcAddress:  row.RPCAddress,
			GatewayPath: row.GatewayPath,
			Scope:       row.Scope,
			Status:      row.Status,
		}
	}
	return items
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
