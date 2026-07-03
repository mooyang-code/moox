package repository

import (
	"context"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *CatalogRepository) ListNodes(ctx context.Context, spaceID string, req *pb.GetNodeListReq) ([]CloudNode, int64, error) {
	q := r.db.WithContext(ctx).Model(&CloudNode{}).Where("c_is_deleted = ?", false)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	if req.GetNodeId() != "" {
		q = q.Where("c_node_id LIKE ?", "%"+req.GetNodeId()+"%")
	}
	if req.GetCloudAccountId() != "" {
		q = q.Where("c_cloud_account_id = ?", req.GetCloudAccountId())
	}
	if req.GetNamespace() != "" {
		q = q.Where("c_namespace = ?", req.GetNamespace())
	}
	if req.GetRegion() != "" {
		q = q.Where("c_region = ?", req.GetRegion())
	}
	if req.GetNodeType() != "" {
		q = q.Where("c_node_type = ?", req.GetNodeType())
	}
	if req.GetStatus() != pb.NodeStatusCode_NODE_STATUS_UNSPECIFIED {
		if status := nodeStatusFromPB(req.GetStatus()); status != "" {
			q = q.Where("c_status = ?", status)
		}
	}
	if req.GetKeyword() != "" {
		kw := "%" + req.GetKeyword() + "%"
		q = q.Where("c_node_id LIKE ? OR c_function_name LIKE ? OR c_metadata LIKE ?", kw, kw, kw)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := pageFromCommon(req.GetPage())
	var nodes []CloudNode
	err := q.Order("c_id DESC").Limit(size).Offset((page - 1) * size).Find(&nodes).Error
	return nodes, total, err
}

func (r *CatalogRepository) GetNode(ctx context.Context, spaceID string, nodeID string) (*CloudNode, error) {
	q := r.db.WithContext(ctx).Where("c_node_id = ? AND c_is_deleted = ?", nodeID, false)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	var node CloudNode
	if err := q.First(&node).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &node, nil
}

func (r *CatalogRepository) UpsertNode(ctx context.Context, node CloudNode) error {
	now := time.Now().UTC()
	if node.CreateTime.IsZero() {
		node.CreateTime = now
	}
	node.ModifyTime = now
	if node.Provider == "" {
		node.Provider = "tencent-scf"
	}
	if node.Status == "" {
		node.Status = "unknown"
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_cloud_account_id", "c_package_id", "c_package_version", "c_deployment_id",
			"c_node_type", "c_region", "c_namespace", "c_function_name", "c_provider",
			"c_running_version", "c_supported_workloads", "c_metadata", "c_status", "c_is_deleted", "c_mtime",
		}),
	}).Create(&node).Error
}

func (r *CatalogRepository) DeleteNodes(ctx context.Context, spaceID string, nodeIDs []string) error {
	if len(nodeIDs) == 0 {
		return nil
	}
	q := r.db.WithContext(ctx).Model(&CloudNode{}).Where("c_node_id IN ?", nodeIDs)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	return q.Updates(map[string]any{"c_is_deleted": true, "c_status": "deleted", "c_mtime": time.Now().UTC()}).Error
}

func (r *CatalogRepository) UpdateNodeDeployment(ctx context.Context, spaceID string, nodeID string, packageID string, packageVersion string) error {
	if nodeID == "" {
		return nil
	}
	updates := map[string]any{
		"c_package_id": packageID,
		"c_mtime":      time.Now().UTC(),
	}
	if packageVersion != "" {
		updates["c_package_version"] = packageVersion
	}
	q := r.db.WithContext(ctx).Model(&CloudNode{}).Where("c_node_id = ? AND c_is_deleted = ?", nodeID, false)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	return q.Updates(updates).Error
}

func (r *CatalogRepository) UpsertHeartbeat(ctx context.Context, spaceID string, nodeID string, nodeType string, version string, supported string, metadata string) error {
	now := time.Now().UTC()
	node := CloudNode{
		SpaceID:            spaceID,
		NodeID:             nodeID,
		NodeType:           nodeType,
		FunctionName:       nodeID,
		RunningVersion:     version,
		SupportedWorkloads: supported,
		Metadata:           metadata,
		Status:             "online",
		LastHeartbeatAt:    &now,
		IsDeleted:          false,
		CreateTime:         now,
		ModifyTime:         now,
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_node_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_node_type", "c_function_name", "c_running_version", "c_supported_workloads",
			"c_metadata", "c_status", "c_last_heartbeat_at", "c_is_deleted", "c_mtime",
		}),
	}).Create(&node).Error
}

func nodeStatusFromPB(status pb.NodeStatusCode) string {
	switch status {
	case pb.NodeStatusCode_NODE_STATUS_ONLINE:
		return "online"
	case pb.NodeStatusCode_NODE_STATUS_TIMEOUT:
		return "timeout"
	case pb.NodeStatusCode_NODE_STATUS_ABNORMAL:
		return "abnormal"
	case pb.NodeStatusCode_NODE_STATUS_OFFLINE:
		return "offline"
	default:
		return ""
	}
}
