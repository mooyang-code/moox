package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrNodeLifecycleMismatch = errors.New("CloudNode lifecycle identity changed")
	ErrNodeDeleteClaimed     = errors.New("CloudNode lifecycle mutation is already claimed")
	ErrNodeMutationClaimed   = errors.New("CloudNode lifecycle mutation claim changed during acquisition")
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
	if req.GetTriggerType() != "" {
		q = q.Where("c_trigger_type = ?", req.GetTriggerType())
	}
	if req.GetBizType() != "" {
		q = q.Where("json_extract(c_metadata, '$.biz_type') = ?", req.GetBizType())
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

// GetNodeIncludingDeleted returns a node identity even when it was soft-deleted.
// Import preview uses this to distinguish a new function from a restorable row.
func (r *CatalogRepository) GetNodeIncludingDeleted(ctx context.Context, spaceID string, nodeID string) (*CloudNode, error) {
	q := r.db.WithContext(ctx).Where("c_node_id = ?", nodeID)
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
	if strings.TrimSpace(node.LifecycleID) == "" {
		node.LifecycleID = uuid.NewString()
	}
	now := r.currentTime()
	if node.CreateTime.IsZero() {
		node.CreateTime = now
	}
	node.ModifyTime = now
	if node.Provider == "" {
		node.Provider = "tencent-scf"
	}
	updates := clause.AssignmentColumns([]string{
		"c_cloud_account_id", "c_package_id", "c_package_version", "c_deployment_id",
		"c_node_type", "c_trigger_type", "c_region", "c_namespace", "c_function_name", "c_provider",
		"c_metadata", "c_is_deleted", "c_mtime",
	})
	updates = append(updates,
		clause.Assignment{Column: clause.Column{Name: "c_lifecycle_id"}, Value: gorm.Expr("CASE WHEN c_is_deleted = 1 OR c_lifecycle_id = '' THEN excluded.c_lifecycle_id ELSE c_lifecycle_id END")},
		clause.Assignment{Column: clause.Column{Name: "c_lifecycle_operation_id"}, Value: gorm.Expr("CASE WHEN c_is_deleted = 1 THEN '' ELSE c_lifecycle_operation_id END")},
	)
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_node_id"}},
		DoUpdates: updates,
		Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "c_lifecycle_operation_id"}, Value: ""}}},
	}).Create(&node)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNodeDeleteClaimed
	}
	return nil
}

// ReserveNodeCreate claims a stable node_id for one durable create request.
// A competing request may only reuse the row when it has the same reservation
// nonce, or replace a soft-deleted row that has no active lifecycle mutation.
func (r *CatalogRepository) ReserveNodeCreate(ctx context.Context, node CloudNode, reservationID string) error {
	reservationID = strings.TrimSpace(reservationID)
	if reservationID == "" || strings.TrimSpace(node.SpaceID) == "" || strings.TrimSpace(node.NodeID) == "" {
		return gorm.ErrInvalidData
	}
	node.LifecycleID = reservationID
	now := r.currentTime()
	if node.CreateTime.IsZero() {
		node.CreateTime = now
	}
	node.ModifyTime = now
	if node.Provider == "" {
		node.Provider = "tencent-scf"
	}
	updates := clause.AssignmentColumns([]string{
		"c_cloud_account_id", "c_package_id", "c_package_version", "c_deployment_id",
		"c_node_type", "c_trigger_type", "c_region", "c_namespace", "c_function_name", "c_provider",
		"c_metadata", "c_is_deleted", "c_mtime",
	})
	updates = append(updates,
		clause.Assignment{Column: clause.Column{Name: "c_lifecycle_id"}, Value: reservationID},
		clause.Assignment{Column: clause.Column{Name: "c_lifecycle_operation_id"}, Value: ""},
	)
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_node_id"}},
		DoUpdates: updates,
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
			SQL:  "((c_is_deleted = 1 AND c_lifecycle_operation_id = '') OR (c_is_deleted = 0 AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ''))",
			Vars: []any{reservationID},
		}}},
	}).Create(&node)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNodeLifecycleMismatch
	}
	return nil
}

// UpdateNodeCreateReservation finalizes a reservation only while it still owns
// the active catalog generation. This prevents a delayed create worker from
// marking a later node generation ready.
func (r *CatalogRepository) UpdateNodeCreateReservation(ctx context.Context, node CloudNode, reservationID string) error {
	reservationID = strings.TrimSpace(reservationID)
	if reservationID == "" || strings.TrimSpace(node.SpaceID) == "" || strings.TrimSpace(node.NodeID) == "" {
		return gorm.ErrInvalidData
	}
	if node.Provider == "" {
		node.Provider = "tencent-scf"
	}
	node.ModifyTime = r.currentTime()
	result := r.db.WithContext(ctx).Model(&CloudNode{}).
		Where("c_space_id = ? AND c_node_id = ? AND c_is_deleted = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ''", node.SpaceID, node.NodeID, false, reservationID).
		Updates(map[string]any{
			"c_cloud_account_id": node.CloudAccountID,
			"c_package_id":       node.PackageID,
			"c_package_version":  node.PackageVersion,
			"c_deployment_id":    node.DeploymentID,
			"c_node_type":        node.NodeType,
			"c_trigger_type":     node.TriggerType,
			"c_region":           node.Region,
			"c_namespace":        node.Namespace,
			"c_function_name":    node.FunctionName,
			"c_provider":         node.Provider,
			"c_metadata":         node.Metadata,
			"c_mtime":            node.ModifyTime,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNodeLifecycleMismatch
	}
	return nil
}

func (r *CatalogRepository) DeleteNodes(ctx context.Context, spaceID string, nodeIDs []string) error {
	if len(nodeIDs) == 0 {
		return nil
	}
	q := r.db.WithContext(ctx).Model(&CloudNode{}).Where("c_node_id IN ?", nodeIDs)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	return q.Updates(map[string]any{"c_is_deleted": true, "c_mtime": time.Now().UTC()}).Error
}

// EnsureNodeLifecycleID assigns a durable generation to a legacy active row.
// The conditional update makes concurrent callers converge on the same value.
func (r *CatalogRepository) EnsureNodeLifecycleID(ctx context.Context, spaceID, nodeID string) (string, error) {
	if strings.TrimSpace(nodeID) == "" {
		return "", gorm.ErrInvalidData
	}
	for attempts := 0; attempts < 3; attempts++ {
		id := uuid.NewString()
		q := r.db.WithContext(ctx).Model(&CloudNode{}).
			Where("c_node_id = ? AND c_is_deleted = ? AND c_lifecycle_id = ''", nodeID, false)
		if spaceID != "" {
			q = q.Where("c_space_id = ?", spaceID)
		}
		if err := q.Updates(map[string]any{"c_lifecycle_id": id, "c_mtime": r.currentTime()}).Error; err != nil {
			return "", err
		}
		node, err := r.GetNode(ctx, spaceID, nodeID)
		if err != nil {
			return "", err
		}
		if node == nil {
			return "", gorm.ErrRecordNotFound
		}
		if node.LifecycleID != "" {
			return node.LifecycleID, nil
		}
	}
	return "", ErrNodeLifecycleMismatch
}

func (r *CatalogRepository) ClaimNodeDelete(ctx context.Context, spaceID, nodeID, lifecycleID, operationID string) error {
	if strings.TrimSpace(lifecycleID) == "" || strings.TrimSpace(operationID) == "" {
		return ErrNodeLifecycleMismatch
	}
	q := r.db.WithContext(ctx).Model(&CloudNode{}).
		Where("c_node_id = ? AND c_lifecycle_id = ? AND (c_lifecycle_operation_id = '' OR c_lifecycle_operation_id = ?)", nodeID, lifecycleID, operationID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	result := q.Updates(map[string]any{"c_lifecycle_operation_id": operationID, "c_mtime": r.currentTime()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return r.nodeMutationClaimConflict(ctx, spaceID, nodeID, lifecycleID, true)
	}
	return nil
}

func (r *CatalogRepository) nodeMutationClaimConflict(ctx context.Context, spaceID, nodeID, lifecycleID string, includeDeleted bool) error {
	var current CloudNode
	query := r.db.WithContext(ctx).Where("c_node_id = ?", nodeID)
	if !includeDeleted {
		query = query.Where("c_is_deleted = ?", false)
	}
	if spaceID != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	if err := query.First(&current).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNodeLifecycleMismatch
	} else if err != nil {
		return err
	}
	if current.LifecycleID != lifecycleID {
		return ErrNodeLifecycleMismatch
	}
	return ErrNodeMutationClaimed
}

// ClaimNodeMutation serializes active-row changes such as deploy. The
// operation id is stable in the durable batch request so a worker can resume
// its own claim after restart without stealing another one.
func (r *CatalogRepository) ClaimNodeMutation(ctx context.Context, spaceID, nodeID, lifecycleID, operationID string) error {
	if strings.TrimSpace(lifecycleID) == "" || strings.TrimSpace(operationID) == "" {
		return ErrNodeLifecycleMismatch
	}
	q := r.db.WithContext(ctx).Model(&CloudNode{}).
		Where("c_node_id = ? AND c_is_deleted = ? AND c_lifecycle_id = ? AND (c_lifecycle_operation_id = '' OR c_lifecycle_operation_id = ?)", nodeID, false, lifecycleID, operationID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	result := q.Updates(map[string]any{"c_lifecycle_operation_id": operationID, "c_mtime": r.currentTime()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return r.nodeMutationClaimConflict(ctx, spaceID, nodeID, lifecycleID, false)
	}
	return nil
}

func (r *CatalogRepository) ReleaseNodeDeleteClaim(ctx context.Context, spaceID, nodeID, lifecycleID, operationID string) error {
	q := r.db.WithContext(ctx).Model(&CloudNode{}).
		Where("c_node_id = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", nodeID, lifecycleID, operationID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	return q.Updates(map[string]any{"c_lifecycle_operation_id": "", "c_mtime": r.currentTime()}).Error
}

func (r *CatalogRepository) ReleaseNodeMutationClaim(ctx context.Context, spaceID, nodeID, lifecycleID, operationID string) error {
	q := r.db.WithContext(ctx).Model(&CloudNode{}).
		Where("c_node_id = ? AND c_is_deleted = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", nodeID, false, lifecycleID, operationID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	return q.Updates(map[string]any{"c_lifecycle_operation_id": "", "c_mtime": r.currentTime()}).Error
}

func (r *CatalogRepository) CompleteNodeDelete(ctx context.Context, spaceID, nodeID, lifecycleID, operationID string) error {
	q := r.db.WithContext(ctx).Model(&CloudNode{}).
		Where("c_node_id = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", nodeID, lifecycleID, operationID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	result := q.Updates(map[string]any{"c_is_deleted": true, "c_mtime": r.currentTime()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNodeLifecycleMismatch
	}
	return nil
}

// DeleteNodesWithPreflight checks and conditionally deletes the entire batch
// in one transaction. The row-generation predicates protect against writers in
// other processes that do not share the in-memory CloudNode locks.
func (r *CatalogRepository) DeleteNodesWithPreflight(ctx context.Context, spaceID string, nodeIDs []string, check func(CloudNode, *FunctionPackage) error) error {
	if len(nodeIDs) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seen := make(map[string]struct{}, len(nodeIDs))
		nodes := make([]CloudNode, 0, len(nodeIDs))
		for _, nodeID := range nodeIDs {
			if _, ok := seen[nodeID]; ok {
				continue
			}
			seen[nodeID] = struct{}{}
			backfill := tx.Model(&CloudNode{}).Where("c_node_id = ? AND c_is_deleted = ? AND c_lifecycle_id = ''", nodeID, false)
			if spaceID != "" {
				backfill = backfill.Where("c_space_id = ?", spaceID)
			}
			if err := backfill.Updates(map[string]any{"c_lifecycle_id": uuid.NewString(), "c_mtime": r.currentTime()}).Error; err != nil {
				return err
			}
			query := tx.Where("c_node_id = ? AND c_is_deleted = ?", nodeID, false)
			if spaceID != "" {
				query = query.Where("c_space_id = ?", spaceID)
			}
			var node CloudNode
			if err := query.First(&node).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			var pkg *FunctionPackage
			if strings.TrimSpace(node.PackageID) != "" {
				var value FunctionPackage
				if err := tx.Where("c_space_id = ? AND c_package_id = ? AND c_is_deleted = ?", node.SpaceID, node.PackageID, false).First(&value).Error; err == nil {
					pkg = &value
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			}
			if check != nil {
				if err := check(node, pkg); err != nil {
					return err
				}
			}
			nodes = append(nodes, node)
		}
		for _, node := range nodes {
			q := tx.Model(&CloudNode{}).Where("c_id = ? AND c_is_deleted = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ''", node.ID, false, node.LifecycleID)
			result := q.Updates(map[string]any{"c_is_deleted": true, "c_mtime": r.currentTime()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrNodeLifecycleMismatch
			}
		}
		return nil
	})
}

// UpdateNodeDeployment persists verified provider state. Lifecycle claims are
// intentionally retained here and released only when the durable batch item
// reaches a terminal state.
func (r *CatalogRepository) UpdateNodeDeployment(ctx context.Context, spaceID string, nodeID string, packageID string, packageVersion string, desiredConfig map[string]string, desiredEnvironment map[string]string, collectorPublishFenced bool, lifecycleMutation ...string) error {
	if nodeID == "" {
		return nil
	}
	if len(lifecycleMutation) != 0 && len(lifecycleMutation) != 2 {
		return gorm.ErrInvalidData
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
	if collectorPublishFenced {
		metadataPatch["biz_type"] = "market_fetcher"
		metadataPatch["collector_publish_fenced"] = true
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
		"storage_timeout_ms":    "MOOX_FETCH_STORAGE_TIMEOUT_MS",
		"max_retry_attempts":    "MOOX_FETCH_MAX_RETRY_ATTEMPTS",
	}
	for metadataKey, environmentKey := range managedEnvironment {
		if value := strings.TrimSpace(desiredEnvironment[environmentKey]); value != "" {
			metadataPatch[metadataKey] = value
		}
	}
	// A timer function's assignment is runtime state owned by Collector, not
	// by the code deployment command. Invalidate the catalog fingerprint after
	// republishing so the next one-minute reconciliation observes the missing
	// or stale remote assignment and writes it back.
	if node.TriggerType == "timer" {
		for _, key := range []string{"assignment_hash", "assignment_count", "dns_hash", "dns_updated_at", "timer_trigger_name", "timer_cron", "timer_enabled", "timer_available_status", "runtime_config_reconciled_at"} {
			metadataPatch[key] = nil
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
	query := r.db.WithContext(ctx).Model(&CloudNode{}).Where("c_id = ? AND c_is_deleted = ?", node.ID, false)
	if len(lifecycleMutation) >= 2 {
		lifecycleID, operationID := strings.TrimSpace(lifecycleMutation[0]), strings.TrimSpace(lifecycleMutation[1])
		if lifecycleID == "" || operationID == "" || lifecycleID != node.LifecycleID {
			return ErrNodeLifecycleMismatch
		}
		query = query.Where("c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", lifecycleID, operationID)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if len(lifecycleMutation) >= 2 && result.RowsAffected == 0 {
		return ErrNodeLifecycleMismatch
	}
	return nil
}

func (r *CatalogRepository) UpdateNodeRuntimeMetadata(ctx context.Context, spaceID, nodeID string, patch map[string]any) error {
	if strings.TrimSpace(nodeID) == "" {
		return gorm.ErrInvalidData
	}
	metadataJSON, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	q := r.db.WithContext(ctx).Model(&CloudNode{}).Where("c_node_id = ? AND c_is_deleted = ?", nodeID, false)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	result := q.Updates(map[string]any{
		"c_metadata": gorm.Expr("json_patch(CASE WHEN json_valid(c_metadata) THEN c_metadata ELSE '{}' END, ?)", string(metadataJSON)),
		"c_mtime":    time.Now().UTC(),
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
