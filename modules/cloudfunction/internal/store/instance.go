package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInstanceLifecycleMismatch = errors.New("FunctionInstance lifecycle identity changed")
	ErrInstanceDeleteClaimed     = errors.New("FunctionInstance lifecycle mutation is already claimed")
	ErrInstanceMutationClaimed   = errors.New("FunctionInstance lifecycle mutation claim changed during acquisition")
)

func (r *CatalogRepository) ListInstances(ctx context.Context, spaceID string, req *pb.ListFunctionInstancesReq) ([]FunctionInstance, int64, error) {
	q := r.db.WithContext(ctx).Model(&FunctionInstance{}).Where("c_is_deleted = ?", false)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	if req.GetInstanceId() != "" {
		q = q.Where("c_instance_id LIKE ?", "%"+req.GetInstanceId()+"%")
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
	if req.GetFunctionType() != "" {
		q = q.Where("c_function_type = ?", req.GetFunctionType())
	}
	if req.GetTriggerType() != "" {
		q = q.Where("c_trigger_type = ?", req.GetTriggerType())
	}
	if req.GetWorkloadType() != "" {
		q = q.Where("json_extract(c_metadata, '$.workload_type') = ?", req.GetWorkloadType())
	}
	if req.GetKeyword() != "" {
		kw := "%" + req.GetKeyword() + "%"
		q = q.Where("c_instance_id LIKE ? OR c_function_name LIKE ? OR c_metadata LIKE ?", kw, kw, kw)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := pageFromCommon(req.GetPage())
	var instances []FunctionInstance
	err := q.Order("c_id DESC").Limit(size).Offset((page - 1) * size).Find(&instances).Error
	return instances, total, err
}

func (r *CatalogRepository) GetInstance(ctx context.Context, spaceID string, instanceID string) (*FunctionInstance, error) {
	q := r.db.WithContext(ctx).Where("c_instance_id = ? AND c_is_deleted = ?", instanceID, false)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	var instance FunctionInstance
	if err := q.First(&instance).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &instance, nil
}

// GetInstanceIncludingDeleted returns a instance identity even when it was soft-deleted.
// Import preview uses this to distinguish a new function from a restorable row.
func (r *CatalogRepository) GetInstanceIncludingDeleted(ctx context.Context, spaceID string, instanceID string) (*FunctionInstance, error) {
	q := r.db.WithContext(ctx).Where("c_instance_id = ?", instanceID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	var instance FunctionInstance
	if err := q.First(&instance).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &instance, nil
}

func (r *CatalogRepository) UpsertInstance(ctx context.Context, instance FunctionInstance) error {
	if strings.TrimSpace(instance.LifecycleID) == "" {
		instance.LifecycleID = uuid.NewString()
	}
	now := r.currentTime()
	if instance.CreateTime.IsZero() {
		instance.CreateTime = now
	}
	instance.ModifyTime = now
	if instance.Provider == "" {
		instance.Provider = "tencent-scf"
	}
	updates := clause.AssignmentColumns([]string{
		"c_cloud_account_id", "c_package_id", "c_package_version", "c_deployment_id",
		"c_function_type", "c_trigger_type", "c_region", "c_namespace", "c_function_name", "c_provider",
		"c_metadata", "c_is_deleted", "c_mtime",
	})
	updates = append(updates,
		clause.Assignment{Column: clause.Column{Name: "c_lifecycle_id"}, Value: gorm.Expr("CASE WHEN c_is_deleted = 1 OR c_lifecycle_id = '' THEN excluded.c_lifecycle_id ELSE c_lifecycle_id END")},
		clause.Assignment{Column: clause.Column{Name: "c_lifecycle_operation_id"}, Value: gorm.Expr("CASE WHEN c_is_deleted = 1 THEN '' ELSE c_lifecycle_operation_id END")},
	)
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_instance_id"}},
		DoUpdates: updates,
		Where:     clause.Where{Exprs: []clause.Expression{clause.Eq{Column: clause.Column{Name: "c_lifecycle_operation_id"}, Value: ""}}},
	}).Create(&instance)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrInstanceDeleteClaimed
	}
	return nil
}

// ReserveInstanceCreate claims a stable instance_id for one durable create request.
// A competing request may only reuse the row when it has the same reservation
// nonce, or replace a soft-deleted row that has no active lifecycle mutation.
func (r *CatalogRepository) ReserveInstanceCreate(ctx context.Context, instance FunctionInstance, reservationID string) error {
	reservationID = strings.TrimSpace(reservationID)
	if reservationID == "" || strings.TrimSpace(instance.SpaceID) == "" || strings.TrimSpace(instance.InstanceID) == "" {
		return gorm.ErrInvalidData
	}
	instance.LifecycleID = reservationID
	now := r.currentTime()
	if instance.CreateTime.IsZero() {
		instance.CreateTime = now
	}
	instance.ModifyTime = now
	if instance.Provider == "" {
		instance.Provider = "tencent-scf"
	}
	updates := clause.AssignmentColumns([]string{
		"c_cloud_account_id", "c_package_id", "c_package_version", "c_deployment_id",
		"c_function_type", "c_trigger_type", "c_region", "c_namespace", "c_function_name", "c_provider",
		"c_metadata", "c_is_deleted", "c_mtime",
	})
	updates = append(updates,
		clause.Assignment{Column: clause.Column{Name: "c_lifecycle_id"}, Value: reservationID},
		clause.Assignment{Column: clause.Column{Name: "c_lifecycle_operation_id"}, Value: ""},
	)
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_instance_id"}},
		DoUpdates: updates,
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
			SQL:  "((c_is_deleted = 1 AND c_lifecycle_operation_id = '') OR (c_is_deleted = 0 AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ''))",
			Vars: []any{reservationID},
		}}},
	}).Create(&instance)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrInstanceLifecycleMismatch
	}
	return nil
}

// UpdateInstanceCreateReservation finalizes a reservation only while it still owns
// the active catalog generation. This prevents a delayed create worker from
// marking a later instance generation ready.
func (r *CatalogRepository) UpdateInstanceCreateReservation(ctx context.Context, instance FunctionInstance, reservationID string) error {
	reservationID = strings.TrimSpace(reservationID)
	if reservationID == "" || strings.TrimSpace(instance.SpaceID) == "" || strings.TrimSpace(instance.InstanceID) == "" {
		return gorm.ErrInvalidData
	}
	if instance.Provider == "" {
		instance.Provider = "tencent-scf"
	}
	instance.ModifyTime = r.currentTime()
	result := r.db.WithContext(ctx).Model(&FunctionInstance{}).
		Where("c_space_id = ? AND c_instance_id = ? AND c_is_deleted = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ''", instance.SpaceID, instance.InstanceID, false, reservationID).
		Updates(map[string]any{
			"c_cloud_account_id": instance.CloudAccountID,
			"c_package_id":       instance.PackageID,
			"c_package_version":  instance.PackageVersion,
			"c_deployment_id":    instance.DeploymentID,
			"c_function_type":    instance.FunctionType,
			"c_trigger_type":     instance.TriggerType,
			"c_region":           instance.Region,
			"c_namespace":        instance.Namespace,
			"c_function_name":    instance.FunctionName,
			"c_provider":         instance.Provider,
			"c_metadata":         instance.Metadata,
			"c_mtime":            instance.ModifyTime,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrInstanceLifecycleMismatch
	}
	return nil
}

func (r *CatalogRepository) DeleteInstances(ctx context.Context, spaceID string, instanceIDs []string) error {
	if len(instanceIDs) == 0 {
		return nil
	}
	q := r.db.WithContext(ctx).Model(&FunctionInstance{}).Where("c_instance_id IN ?", instanceIDs)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	return q.Updates(map[string]any{"c_is_deleted": true, "c_mtime": time.Now().UTC()}).Error
}

// InstanceLifecycleID 返回活动实例的生命周期 ID（实例创建时分配）。
func (r *CatalogRepository) InstanceLifecycleID(ctx context.Context, spaceID, instanceID string) (string, error) {
	if strings.TrimSpace(instanceID) == "" {
		return "", gorm.ErrInvalidData
	}
	instance, err := r.GetInstance(ctx, spaceID, instanceID)
	if err != nil {
		return "", err
	}
	if instance == nil || instance.IsDeleted {
		return "", gorm.ErrRecordNotFound
	}
	if instance.LifecycleID == "" {
		return "", ErrInstanceLifecycleMismatch
	}
	return instance.LifecycleID, nil
}

func (r *CatalogRepository) ClaimInstanceDelete(ctx context.Context, spaceID, instanceID, lifecycleID, operationID string) error {
	if strings.TrimSpace(lifecycleID) == "" || strings.TrimSpace(operationID) == "" {
		return ErrInstanceLifecycleMismatch
	}
	q := r.db.WithContext(ctx).Model(&FunctionInstance{}).
		Where("c_instance_id = ? AND c_lifecycle_id = ? AND (c_lifecycle_operation_id = '' OR c_lifecycle_operation_id = ?)", instanceID, lifecycleID, operationID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	result := q.Updates(map[string]any{"c_lifecycle_operation_id": operationID, "c_mtime": r.currentTime()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return r.instanceMutationClaimConflict(ctx, spaceID, instanceID, lifecycleID, true)
	}
	return nil
}

func (r *CatalogRepository) instanceMutationClaimConflict(ctx context.Context, spaceID, instanceID, lifecycleID string, includeDeleted bool) error {
	var current FunctionInstance
	query := r.db.WithContext(ctx).Where("c_instance_id = ?", instanceID)
	if !includeDeleted {
		query = query.Where("c_is_deleted = ?", false)
	}
	if spaceID != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	if err := query.First(&current).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrInstanceLifecycleMismatch
	} else if err != nil {
		return err
	}
	if current.LifecycleID != lifecycleID {
		return ErrInstanceLifecycleMismatch
	}
	return ErrInstanceMutationClaimed
}

// ClaimInstanceMutation serializes active-row changes such as deploy. The
// operation id is stable in the durable batch request so a worker can resume
// its own claim after restart without stealing another one.
func (r *CatalogRepository) ClaimInstanceMutation(ctx context.Context, spaceID, instanceID, lifecycleID, operationID string) error {
	if strings.TrimSpace(lifecycleID) == "" || strings.TrimSpace(operationID) == "" {
		return ErrInstanceLifecycleMismatch
	}
	q := r.db.WithContext(ctx).Model(&FunctionInstance{}).
		Where("c_instance_id = ? AND c_is_deleted = ? AND c_lifecycle_id = ? AND (c_lifecycle_operation_id = '' OR c_lifecycle_operation_id = ?)", instanceID, false, lifecycleID, operationID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	result := q.Updates(map[string]any{"c_lifecycle_operation_id": operationID, "c_mtime": r.currentTime()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return r.instanceMutationClaimConflict(ctx, spaceID, instanceID, lifecycleID, false)
	}
	return nil
}

func (r *CatalogRepository) ReleaseInstanceDeleteClaim(ctx context.Context, spaceID, instanceID, lifecycleID, operationID string) error {
	q := r.db.WithContext(ctx).Model(&FunctionInstance{}).
		Where("c_instance_id = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", instanceID, lifecycleID, operationID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	return q.Updates(map[string]any{"c_lifecycle_operation_id": "", "c_mtime": r.currentTime()}).Error
}

func (r *CatalogRepository) ReleaseInstanceMutationClaim(ctx context.Context, spaceID, instanceID, lifecycleID, operationID string) error {
	q := r.db.WithContext(ctx).Model(&FunctionInstance{}).
		Where("c_instance_id = ? AND c_is_deleted = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", instanceID, false, lifecycleID, operationID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	return q.Updates(map[string]any{"c_lifecycle_operation_id": "", "c_mtime": r.currentTime()}).Error
}

func (r *CatalogRepository) CompleteInstanceDelete(ctx context.Context, spaceID, instanceID, lifecycleID, operationID string) error {
	q := r.db.WithContext(ctx).Model(&FunctionInstance{}).
		Where("c_instance_id = ? AND c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", instanceID, lifecycleID, operationID)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	result := q.Updates(map[string]any{"c_is_deleted": true, "c_mtime": r.currentTime()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrInstanceLifecycleMismatch
	}
	return nil
}

func (r *CatalogRepository) UpdateInstanceDeployment(ctx context.Context, spaceID string, instanceID string, packageID string, packageVersion string, desiredConfig map[string]string, desiredEnvironment map[string]string, collectorPublishFenced bool, lifecycleMutation ...string) error {
	if instanceID == "" {
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
	q := r.db.WithContext(ctx).Model(&FunctionInstance{}).Where("c_instance_id = ? AND c_is_deleted = ?", instanceID, false)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	var instance FunctionInstance
	if err := q.First(&instance).Error; err != nil {
		return err
	}
	existingMetadata := map[string]any{}
	if strings.TrimSpace(instance.Metadata) != "" {
		_ = json.Unmarshal([]byte(instance.Metadata), &existingMetadata)
	}
	metadataPatch := map[string]any{"deployment_ready": true}
	if collectorPublishFenced {
		metadataPatch["workload_type"] = "market_fetcher"
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
	// republishing an existing instance.
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
	if instance.TriggerType == "timer" {
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
	// making c_instance_id ambiguous.
	query := r.db.WithContext(ctx).Model(&FunctionInstance{}).Where("c_id = ? AND c_is_deleted = ?", instance.ID, false)
	if len(lifecycleMutation) >= 2 {
		lifecycleID, operationID := strings.TrimSpace(lifecycleMutation[0]), strings.TrimSpace(lifecycleMutation[1])
		if lifecycleID == "" || operationID == "" || lifecycleID != instance.LifecycleID {
			return ErrInstanceLifecycleMismatch
		}
		query = query.Where("c_lifecycle_id = ? AND c_lifecycle_operation_id = ?", lifecycleID, operationID)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if len(lifecycleMutation) >= 2 && result.RowsAffected == 0 {
		return ErrInstanceLifecycleMismatch
	}
	return nil
}

func (r *CatalogRepository) UpdateInstanceRuntimeMetadata(ctx context.Context, spaceID, instanceID string, patch map[string]any) error {
	if strings.TrimSpace(instanceID) == "" {
		return gorm.ErrInvalidData
	}
	metadataJSON, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	q := r.db.WithContext(ctx).Model(&FunctionInstance{}).Where("c_instance_id = ? AND c_is_deleted = ?", instanceID, false)
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
