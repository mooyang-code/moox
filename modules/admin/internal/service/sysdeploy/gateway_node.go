package sysdeploy

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"gorm.io/gorm"
)

var nodeIDPattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

type GatewayNodeFilter struct{ NodeID, Status string }

type GatewayStatusReport struct {
	NodeID           string
	AppliedRouteHash string
	RouteCount       int32
	LastSeenAt       time.Time
	LastError        string
}

func (s *ServiceImpl) ListGatewayNodes(ctx context.Context, req *pb.ListGatewayNodesReq) (*pb.ListGatewayNodesRsp, error) {
	pageNo, offset, limit := normalizePage(req.GetPage())
	rows, total, err := s.dao.ListGatewayNodes(ctx, GatewayNodeFilter{NodeID: req.GetNodeId(), Status: req.GetStatus()}, offset, limit)
	if err != nil {
		return &pb.ListGatewayNodesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	items := make([]*pb.GatewayNode, 0, len(rows))
	for i := range rows {
		items = append(items, gatewayNodeToPB(&rows[i]))
	}
	return &pb.ListGatewayNodesRsp{RetInfo: retOK(), Nodes: items, PageResult: makePageResult(pageNo, limit, total)}, nil
}

func (s *ServiceImpl) CreateGatewayNode(ctx context.Context, req *pb.CreateGatewayNodeReq) (*pb.CreateGatewayNodeRsp, error) {
	node := pbToGatewayNode(req.GetNode())
	if err := validateGatewayNode(node); err != nil {
		return &pb.CreateGatewayNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.dao.CreateGatewayNode(ctx, node); err != nil {
		return &pb.CreateGatewayNodeRsp{RetInfo: retErr(gatewayNodeDAOErrorCode(err), err.Error())}, nil
	}
	return &pb.CreateGatewayNodeRsp{RetInfo: retOK(), Node: gatewayNodeToPB(node)}, nil
}

func (s *ServiceImpl) UpdateGatewayNode(ctx context.Context, req *pb.UpdateGatewayNodeReq) (*pb.UpdateGatewayNodeRsp, error) {
	node := pbToGatewayNode(req.GetNode())
	if node == nil {
		return &pb.UpdateGatewayNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node is required")}, nil
	}
	node.NodeID = req.GetNodeId()
	if err := validateGatewayNode(node); err != nil {
		return &pb.UpdateGatewayNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.dao.UpdateGatewayNode(ctx, node.NodeID, node); err != nil {
		code := gatewayNodeDAOErrorCode(err)
		return &pb.UpdateGatewayNodeRsp{RetInfo: retErr(code, err.Error())}, nil
	}
	row, _ := s.dao.GetGatewayNode(ctx, node.NodeID)
	return &pb.UpdateGatewayNodeRsp{RetInfo: retOK(), Node: gatewayNodeToPB(row)}, nil
}

func (s *ServiceImpl) DeleteGatewayNode(ctx context.Context, req *pb.DeleteGatewayNodeReq) (*pb.DeleteGatewayNodeRsp, error) {
	if req.GetNodeId() == "" {
		return &pb.DeleteGatewayNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	if err := s.dao.DeleteGatewayNode(ctx, req.GetNodeId()); err != nil {
		code := gatewayNodeDAOErrorCode(err)
		return &pb.DeleteGatewayNodeRsp{RetInfo: retErr(code, err.Error())}, nil
	}
	return &pb.DeleteGatewayNodeRsp{RetInfo: retOK()}, nil
}

func gatewayNodeDAOErrorCode(err error) pb.ErrorCode {
	if err == gorm.ErrRecordNotFound {
		return pb.ErrorCode_NOT_FOUND
	}
	message := err.Error()
	if strings.Contains(message, "gateway node already exists") || strings.Contains(message, "ssh host not found") || strings.Contains(message, "has ") && strings.Contains(message, "service deployments") {
		return pb.ErrorCode_INVALID_PARAM
	}
	return pb.ErrorCode_INNER_ERR
}

func (s *ServiceImpl) GetGatewayNodeRoutes(ctx context.Context, req *pb.GetGatewayNodeRoutesReq) (*pb.GetGatewayNodeRoutesRsp, error) {
	if req.GetNodeId() == "" {
		return &pb.GetGatewayNodeRoutesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	snapshot, err := s.CompileGatewaySnapshot(ctx, req.GetNodeId())
	if err != nil {
		code := pb.ErrorCode_INVALID_PARAM
		if err == gorm.ErrRecordNotFound {
			code = pb.ErrorCode_NOT_FOUND
		}
		return &pb.GetGatewayNodeRoutesRsp{RetInfo: retErr(code, err.Error())}, nil
	}
	routes := make([]*pb.GatewayRoute, 0, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		routes = append(routes, &pb.GatewayRoute{ServiceId: route.ServiceID, Address: route.Address, ServicePath: route.ServicePath, TimeoutMs: int32(route.TimeoutMS), MaxBodyBytes: route.MaxBodyBytes})
	}
	return &pb.GetGatewayNodeRoutesRsp{RetInfo: retOK(), NodeId: snapshot.NodeID, RouteHash: snapshot.RouteHash, GeneratedAt: snapshot.GeneratedAt.Format(time.RFC3339Nano), Disabled: snapshot.Disabled, Routes: routes}, nil
}

func pbToGatewayNode(item *pb.GatewayNode) *GatewayNode {
	if item == nil {
		return nil
	}
	node := &GatewayNode{NodeID: item.GetNodeId(), Name: item.GetName(), PublicAddress: item.GetPublicAddress(), Status: item.GetStatus(), RouteHash: item.GetRouteHash(), AppliedRouteHash: item.GetAppliedRouteHash(), RouteCount: item.GetRouteCount(), LastError: item.GetLastError()}
	node.LastSeenAt = parsePBTime(item.GetLastSeenAt())
	node.CreatedAt = parsePBTime(item.GetCreatedAt())
	node.UpdatedAt = parsePBTime(item.GetUpdatedAt())
	if item.GetHostId() > 0 {
		hostID := item.GetHostId()
		node.HostID = &hostID
	}
	return node
}

func parsePBTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}
func gatewayNodeToPB(node *GatewayNode) *pb.GatewayNode {
	if node == nil {
		return nil
	}
	item := &pb.GatewayNode{NodeId: node.NodeID, Name: node.Name, PublicAddress: node.PublicAddress, Status: node.Status, RouteHash: node.RouteHash, AppliedRouteHash: node.AppliedRouteHash, RouteCount: node.RouteCount, LastSeenAt: formatTimePtr(node.LastSeenAt), LastError: node.LastError, CreatedAt: formatTimePtr(node.CreatedAt), UpdatedAt: formatTimePtr(node.UpdatedAt)}
	if node.HostID != nil {
		item.HostId = *node.HostID
	}
	return item
}

func validateGatewayNode(node *GatewayNode) error {
	if node == nil {
		return fmt.Errorf("node is required")
	}
	node.NodeID, node.Name, node.PublicAddress, node.Status = strings.TrimSpace(node.NodeID), strings.TrimSpace(node.Name), strings.TrimSpace(node.PublicAddress), strings.TrimSpace(node.Status)
	if !nodeIDPattern.MatchString(node.NodeID) {
		return fmt.Errorf("node_id must use lowercase letters, digits, dash, or underscore")
	}
	if node.Name == "" {
		return fmt.Errorf("name is required")
	}
	u, err := url.Parse(node.PublicAddress)
	loopbackDev := u != nil && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost")
	// Plain HTTP is restricted to explicit loopback development addresses.
	if err != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "https" && !loopbackDev) {
		return fmt.Errorf("public_address must be an absolute HTTPS URL or loopback HTTP development URL")
	}
	if node.Status == "" {
		node.Status = "enabled"
	}
	if node.Status != "enabled" && node.Status != "disabled" {
		return fmt.Errorf("status must be enabled or disabled")
	}
	if node.HostID != nil && *node.HostID <= 0 {
		return fmt.Errorf("host_id must be positive")
	}
	return nil
}

func (d *DAO) CreateGatewayNode(ctx context.Context, node *GatewayNode) error {
	if err := validateGatewayNode(node); err != nil {
		return err
	}
	if err := d.validateGatewayNodeHost(ctx, node.HostID); err != nil {
		return err
	}
	now := time.Now()
	node.CreatedAt, node.UpdatedAt = &now, &now
	if err := d.db.WithContext(ctx).Create(node).Error; err != nil {
		if isUniqueConstraintError(err) {
			return fmt.Errorf("gateway node already exists: %s", node.NodeID)
		}
		return err
	}
	return nil
}

func (d *DAO) GetGatewayNode(ctx context.Context, nodeID string) (*GatewayNode, error) {
	if strings.TrimSpace(nodeID) == "" {
		return nil, fmt.Errorf("node_id is required")
	}
	var node GatewayNode
	if err := d.db.WithContext(ctx).Where("c_node_id = ?", strings.TrimSpace(nodeID)).First(&node).Error; err != nil {
		return nil, err
	}
	return &node, nil
}

func (d *DAO) UpdateGatewayNode(ctx context.Context, nodeID string, node *GatewayNode) error {
	if node == nil {
		return fmt.Errorf("node is required")
	}
	node.NodeID = strings.TrimSpace(nodeID)
	if err := validateGatewayNode(node); err != nil {
		return err
	}
	if err := d.validateGatewayNodeHost(ctx, node.HostID); err != nil {
		return err
	}
	result := d.db.WithContext(ctx).Model(&GatewayNode{}).Where("c_node_id = ?", nodeID).Updates(map[string]interface{}{"c_host_id": node.HostID, "c_name": node.Name, "c_public_address": node.PublicAddress, "c_status": node.Status, "c_mtime": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (d *DAO) validateGatewayNodeHost(ctx context.Context, hostID *int64) error {
	if hostID == nil {
		return nil
	}
	var count int64
	if err := d.db.WithContext(ctx).Table("t_ssh_host").Where("c_id = ?", *hostID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("ssh host not found: %d", *hostID)
	}
	return nil
}

func (d *DAO) DeleteGatewayNode(ctx context.Context, nodeID string) error {
	var count int64
	if err := d.db.WithContext(ctx).Model(&Deployment{}).Where("c_node_id = ?", nodeID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("gateway node %s has %d service deployments", nodeID, count)
	}
	result := d.db.WithContext(ctx).Where("c_node_id = ?", nodeID).Delete(&GatewayNode{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (d *DAO) ListGatewayNodes(ctx context.Context, filter GatewayNodeFilter, offset, limit int) ([]GatewayNode, int64, error) {
	query := d.db.WithContext(ctx).Model(&GatewayNode{})
	if filter.NodeID != "" {
		query = query.Where("c_node_id LIKE ?", "%"+filter.NodeID+"%")
	}
	if filter.Status != "" {
		query = query.Where("c_status = ?", filter.Status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []GatewayNode
	if err := query.Order("c_node_id ASC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (d *DAO) ReportGatewayStatus(ctx context.Context, report GatewayStatusReport) error {
	if report.NodeID == "" {
		return fmt.Errorf("node_id is required")
	}
	if report.LastSeenAt.IsZero() {
		report.LastSeenAt = time.Now().UTC()
	}
	result := d.db.WithContext(ctx).Model(&GatewayNode{}).
		Where("c_node_id = ? AND (c_last_seen_at IS NULL OR c_last_seen_at <= ?)", report.NodeID, report.LastSeenAt).
		Updates(map[string]interface{}{"c_applied_route_hash": report.AppliedRouteHash, "c_route_count": report.RouteCount, "c_last_seen_at": report.LastSeenAt, "c_last_error": report.LastError, "c_mtime": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		var count int64
		if err := d.db.WithContext(ctx).Model(&GatewayNode{}).Where("c_node_id = ?", report.NodeID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	}
	return nil
}
