package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const scfHeartbeatTimeout = 90 * time.Second

// ListSCFEventNodes returns every live catalog entry backed by Tencent SCF's
// event invocation mode. Node status is intentionally not part of the filter:
// newly published functions need keepalives before their first heartbeat.
func (r *CatalogRepository) ListSCFEventNodes(ctx context.Context) ([]CloudNode, error) {
	var nodes []CloudNode
	err := r.db.WithContext(ctx).
		Where("c_is_deleted = ? AND c_provider = ? AND c_node_type = ?", false, "tencent-scf", "scf-event").
		Where("CASE WHEN json_valid(c_metadata) THEN COALESCE(json_extract(c_metadata, '$.biz_type'), '') ELSE '' END <> ?", "market_fetcher").
		Order("c_id ASC").
		Find(&nodes).Error
	return nodes, err
}

func (r *CatalogRepository) ListNodes(ctx context.Context, spaceID string, req *pb.GetNodeListReq) ([]CloudNode, int64, error) {
	q := r.db.WithContext(ctx).Model(&CloudNode{}).Where("c_is_deleted = ?", false)
	now := r.currentTime()
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
	if req.GetBizType() != "" {
		q = q.Where("json_extract(c_metadata, '$.biz_type') = ?", req.GetBizType())
	}
	if req.GetStatus() != pb.NodeStatusCode_NODE_STATUS_UNSPECIFIED {
		switch req.GetStatus() {
		case pb.NodeStatusCode_NODE_STATUS_ONLINE:
			q = q.Where("c_last_heartbeat_at IS NOT NULL AND c_last_heartbeat_at >= ?", now.Add(-scfHeartbeatTimeout))
		case pb.NodeStatusCode_NODE_STATUS_TIMEOUT:
			q = q.Where("c_last_heartbeat_at IS NOT NULL AND c_last_heartbeat_at < ?", now.Add(-scfHeartbeatTimeout))
		default:
			status := nodeStatusFromPB(req.GetStatus())
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
	for index := range nodes {
		deriveSCFHeartbeatStatus(&nodes[index], now)
	}
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
	deriveSCFHeartbeatStatus(&node, r.currentTime())
	return &node, nil
}

func (r *CatalogRepository) UpsertNode(ctx context.Context, node CloudNode) error {
	now := r.currentTime()
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
			"c_last_heartbeat_at",
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

func (r *CatalogRepository) UpdateNodeDeployment(ctx context.Context, spaceID string, nodeID string, packageID string, packageVersion string, desiredConfig map[string]string, desiredEnvironment map[string]string) error {
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
	var node CloudNode
	if err := q.First(&node).Error; err != nil {
		return err
	}
	existingMetadata := map[string]any{}
	if strings.TrimSpace(node.Metadata) != "" {
		_ = json.Unmarshal([]byte(node.Metadata), &existingMetadata)
	}
	metadataPatch := map[string]any{"deployment_ready": true}
	if bizType, _ := existingMetadata["biz_type"].(string); strings.EqualFold(strings.TrimSpace(bizType), "market_fetcher") {
		// Deployment updates bypass UpdateNode, so clear stale resident worker
		// queue identities here as well when a Fetcher is republished.
		updates["c_supported_workloads"] = "[]"
		metadataPatch["supported_workloads"] = []string{}
	}
	if len(desiredConfig) > 0 {
		metadataPatch["config"] = desiredConfig
		if value := strings.TrimSpace(desiredConfig["memory_size"]); value != "" {
			metadataPatch["memory_size"] = value
		}
		if value := strings.TrimSpace(desiredConfig["timeout"]); value != "" {
			metadataPatch["timeout_seconds"] = value
		}
	}
	// Deployment updates carry the fetcher settings in the function
	// environment. Mirror the managed values into catalog metadata so fleet
	// inspection and the scheduler see the same effective configuration after
	// republishing an existing node.
	managedEnvironment := map[string]string{
		"max_inflight_requests": "MOOX_FETCH_MAX_INFLIGHT_REQUESTS",
		"request_timeout_ms":    "MOOX_FETCH_REQUEST_TIMEOUT_MS",
		"http_max_attempts":     "MOOX_FETCH_HTTP_MAX_ATTEMPTS",
		"storage_max_attempts":  "MOOX_FETCH_STORAGE_MAX_ATTEMPTS",
		"realtime_batch_size":   "MOOX_FETCH_REALTIME_BATCH_SIZE",
		"realtime_bar_limit":    "MOOX_FETCH_REALTIME_BAR_LIMIT",
		"catchup_batch_size":    "MOOX_FETCH_CATCHUP_BATCH_SIZE",
		"catchup_bar_limit":     "MOOX_FETCH_CATCHUP_BAR_LIMIT",
		"commit_reserve_ms":     "MOOX_FETCH_COMMIT_RESERVE_MS",
		"max_retry_attempts":    "MOOX_FETCH_MAX_RETRY_ATTEMPTS",
	}
	for metadataKey, environmentKey := range managedEnvironment {
		if value := strings.TrimSpace(desiredEnvironment[environmentKey]); value != "" {
			metadataPatch[metadataKey] = value
		}
	}
	metadataJSON, err := json.Marshal(metadataPatch)
	if err != nil {
		return err
	}
	updates["c_metadata"] = gorm.Expr(
		"json_patch(CASE WHEN json_valid(c_metadata) THEN c_metadata ELSE '{}' END, ?)",
		string(metadataJSON),
	)
	// Rebuild the update query from the primary key. Reusing the SELECT scope
	// causes GORM's SQLite dialector to emit an UPDATE ... FROM self join,
	// making c_node_id ambiguous.
	return r.db.WithContext(ctx).Model(&CloudNode{}).Where("c_id = ?", node.ID).Updates(updates).Error
}

func (r *CatalogRepository) UpdateHeartbeat(ctx context.Context, spaceID string, nodeID string, version string, supported string, metadata string) error {
	now := r.currentTime()
	return r.db.WithContext(ctx).Model(&CloudNode{}).
		Where("c_space_id = ? AND c_node_id = ? AND c_is_deleted = ?", spaceID, nodeID, false).
		Updates(map[string]any{
			"c_running_version":     version,
			"c_supported_workloads": supported,
			"c_metadata": gorm.Expr(
				"json_patch(CASE WHEN json_valid(c_metadata) THEN c_metadata ELSE '{}' END, ?)",
				metadata,
			),
			"c_status":            "online",
			"c_last_heartbeat_at": now,
			"c_mtime":             now,
		}).Error
}

func deriveSCFHeartbeatStatus(node *CloudNode, now time.Time) {
	if node == nil || strings.EqualFold(node.Status, "deleted") {
		return
	}
	node.Status = SCFHeartbeatStatus(node.LastHeartbeatAt, now)
}

func SCFHeartbeatStatus(lastHeartbeat *time.Time, now time.Time) string {
	if lastHeartbeat == nil || lastHeartbeat.IsZero() {
		return "unknown"
	}
	if now.UTC().Sub(lastHeartbeat.UTC()) > scfHeartbeatTimeout {
		return "timeout"
	}
	return "online"
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
