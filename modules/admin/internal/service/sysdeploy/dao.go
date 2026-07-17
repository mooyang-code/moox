package sysdeploy

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type DAO struct {
	db *gorm.DB
}

func NewDAO(db *gorm.DB) *DAO { return &DAO{db: db} }

func (d *DAO) Create(ctx context.Context, item *Deployment) error {
	if item == nil {
		return fmt.Errorf("deployment is required")
	}
	normalizeDeployment(item)
	if item.NodeID == "" || item.ServiceName == "" {
		return fmt.Errorf("node_id and service_name are required")
	}
	if exists, err := d.exists(ctx, item.NodeID, item.ServiceName); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("service deployment already exists: %s/%s", item.NodeID, item.ServiceName)
	}
	now := time.Now()
	item.CreatedAt = now
	item.UpdatedAt = now
	if err := d.db.WithContext(ctx).Create(item).Error; err != nil {
		if isUniqueConstraintError(err) {
			return fmt.Errorf("service deployment already exists: %s", item.ServiceName)
		}
		return err
	}
	return nil
}

func (d *DAO) Update(ctx context.Context, nodeID, serviceName string, item *Deployment) error {
	nodeID = strings.TrimSpace(nodeID)
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return fmt.Errorf("service_name is required")
	}
	if item == nil {
		return fmt.Errorf("deployment is required")
	}
	item.NodeID, item.ServiceName = nodeID, serviceName
	normalizeDeployment(item)
	result := d.db.WithContext(ctx).Model(&Deployment{}).
		Where("c_node_id = ? AND c_service_name = ?", nodeID, serviceName).
		Updates(map[string]interface{}{
			"c_service_kind":       item.ServiceKind,
			"c_protocol":           item.Protocol,
			"c_host":               item.Host,
			"c_port":               item.Port,
			"c_gateway_path":       item.GatewayPath,
			"c_gateway_service_id": item.GatewayServiceID,
			"c_gateway_enabled":    item.GatewayEnabled,
			"c_scope":              item.Scope,
			"c_status":             item.Status,
			"c_description":        item.Description,
			"c_extra_config":       item.ExtraConfig,
			"c_mtime":              time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("service deployment not found: %s: %w", serviceName, gorm.ErrRecordNotFound)
	}
	return nil
}

func (d *DAO) Delete(ctx context.Context, nodeID, serviceName string) error {
	nodeID = strings.TrimSpace(nodeID)
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return fmt.Errorf("service_name is required")
	}
	result := d.db.WithContext(ctx).
		Where("c_node_id = ? AND c_service_name = ?", nodeID, serviceName).
		Delete(&Deployment{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("service deployment not found: %s: %w", serviceName, gorm.ErrRecordNotFound)
	}
	return nil
}

func (d *DAO) Get(ctx context.Context, nodeID, serviceName string) (*Deployment, error) {
	nodeID = strings.TrimSpace(nodeID)
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return nil, fmt.Errorf("service_name is required")
	}
	var row Deployment
	err := d.db.WithContext(ctx).Where("c_node_id = ? AND c_service_name = ?", nodeID, serviceName).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (d *DAO) List(ctx context.Context, filter ListFilter, offset int, limit int) ([]Deployment, int64, error) {
	query := d.db.WithContext(ctx).Model(&Deployment{})
	if filter.NodeID != "" {
		query = query.Where("c_node_id = ?", filter.NodeID)
	}
	if filter.ServiceName != "" {
		query = query.Where("c_service_name LIKE ?", "%"+filter.ServiceName+"%")
	}
	if filter.ServiceKind != "" {
		query = query.Where("c_service_kind = ?", filter.ServiceKind)
	}
	if filter.Scope != "" {
		query = query.Where("c_scope = ?", filter.Scope)
	}
	if filter.Status != "" {
		query = query.Where("c_status = ?", filter.Status)
	}
	if filter.GatewayEnabled != nil {
		query = query.Where("c_gateway_enabled = ?", *filter.GatewayEnabled)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Deployment
	if err := query.Order("c_node_id ASC, c_scope DESC, c_service_kind ASC, c_service_name ASC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (d *DAO) ListActive(ctx context.Context, nodeID string) ([]Deployment, error) {
	var rows []Deployment
	query := d.db.WithContext(ctx).Where("c_status = ?", "active")
	if nodeID != "" {
		query = query.Where("c_node_id = ?", nodeID)
	}
	err := query.Order("c_node_id ASC, c_scope DESC, c_service_kind ASC, c_service_name ASC").
		Find(&rows).Error
	return rows, err
}

func (d *DAO) SeedDefaults(ctx context.Context, rows []Deployment) error {
	if err := d.retireLegacyAdminMonitor(ctx); err != nil {
		return err
	}
	if err := d.retireSplitViewDeployments(ctx); err != nil {
		return err
	}
	if err := d.db.WithContext(ctx).Where("c_service_name IN ?", []string{"service_gateway", "service_gateway_internal"}).Delete(&Deployment{}).Error; err != nil {
		return err
	}
	for i := range rows {
		item := rows[i]
		normalizeDeployment(&item)
		if item.ServiceName == "" {
			continue
		}
		exists, err := d.exists(ctx, item.NodeID, item.ServiceName)
		if err != nil {
			return err
		}
		if exists {
			if err := d.backfillDefaultExtraConfig(ctx, &item); err != nil {
				return err
			}
			continue
		}
		if err := d.Create(ctx, &item); err != nil {
			return err
		}
	}
	return nil
}

func (d *DAO) retireSplitViewDeployments(ctx context.Context) error {
	return d.db.WithContext(ctx).
		Where("c_service_name IN ?", []string{"storage_view_builder", "storage_view_query", "storage_view_index"}).
		Delete(&Deployment{}).Error
}

func (d *DAO) retireLegacyAdminMonitor(ctx context.Context) error {
	const where = "c_service_name = ? AND c_service_kind = ? AND c_gateway_path = ? AND c_port = ?"
	return d.db.WithContext(ctx).
		Where(where, "monitor", "admin_rpc", "trpc.moox.ops.Monitor", 11103).
		Delete(&Deployment{}).Error
}

func (d *DAO) backfillDefaultExtraConfig(ctx context.Context, item *Deployment) error {
	row, err := d.Get(ctx, item.NodeID, item.ServiceName)
	if err != nil {
		return err
	}
	existingExtra, migrated := migrateUnifiedStorageViewHealth(item.ServiceName, row.ExtraConfig, item.ExtraConfig)
	next, changed := mergeDefaultExtraConfig(existingExtra, item.ExtraConfig)
	changed = changed || migrated
	updates := map[string]interface{}{}
	if changed {
		updates["c_extra_config"] = next
	}
	if row.GatewayEnabled != item.GatewayEnabled {
		updates["c_gateway_enabled"] = item.GatewayEnabled
	}
	if row.GatewayServiceID != item.GatewayServiceID {
		updates["c_gateway_service_id"] = item.GatewayServiceID
	}
	if len(updates) == 0 {
		return nil
	}
	updates["c_mtime"] = time.Now()
	return d.db.WithContext(ctx).Model(&Deployment{}).
		Where("c_node_id = ? AND c_service_name = ?", item.NodeID, item.ServiceName).
		Updates(updates).Error
}

func migrateUnifiedStorageViewHealth(serviceName, existingRaw, defaultRaw string) (string, bool) {
	if serviceName != "storage_view" {
		return existingRaw, false
	}
	existing, defaults := map[string]interface{}{}, map[string]interface{}{}
	if json.Unmarshal([]byte(existingRaw), &existing) != nil || json.Unmarshal([]byte(defaultRaw), &defaults) != nil {
		return existingRaw, false
	}
	if existing["health_url"] != "http://127.0.0.1:20212/readyz" || defaults["health_url"] == nil {
		return existingRaw, false
	}
	existing["health_url"] = defaults["health_url"]
	raw, err := json.Marshal(existing)
	if err != nil {
		return existingRaw, false
	}
	return string(raw), true
}

func mergeDefaultExtraConfig(existingRaw, defaultRaw string) (string, bool) {
	defaultRaw = strings.TrimSpace(defaultRaw)
	if defaultRaw == "" || defaultRaw == "{}" {
		return existingRaw, false
	}
	defaults := map[string]interface{}{}
	if err := json.Unmarshal([]byte(defaultRaw), &defaults); err != nil || len(defaults) == 0 {
		return existingRaw, false
	}
	existing := map[string]interface{}{}
	existingRaw = strings.TrimSpace(existingRaw)
	if existingRaw != "" {
		_ = json.Unmarshal([]byte(existingRaw), &existing)
	}
	_, originalKindConfigured := existing["health_kind"]
	changed := false
	for key, value := range defaults {
		if _, ok := existing[key]; ok {
			continue
		}
		existing[key] = value
		changed = true
	}
	if existingURL, ok := existing["health_url"].(string); ok {
		defaultURL, defaultOK := defaults["health_url"].(string)
		if defaultOK && !originalKindConfigured && strings.HasSuffix(strings.TrimRight(existingURL, "/"), "/healthz") && strings.HasSuffix(strings.TrimRight(defaultURL, "/"), "/readyz") {
			existing["health_url"] = defaultURL
			existing["health_kind"] = "readiness"
			changed = true
		}
	}
	if !changed {
		return existingRaw, false
	}
	raw, err := json.Marshal(existing)
	if err != nil {
		return existingRaw, false
	}
	return string(raw), true
}

func (d *DAO) exists(ctx context.Context, nodeID, serviceName string) (bool, error) {
	var count int64
	err := d.db.WithContext(ctx).Model(&Deployment{}).
		Where("c_node_id = ? AND c_service_name = ?", nodeID, serviceName).
		Count(&count).Error
	return count > 0, err
}

type ListFilter struct {
	NodeID         string
	ServiceName    string
	ServiceKind    string
	Scope          string
	Status         string
	GatewayEnabled *bool
}

func normalizeDeployment(item *Deployment) {
	item.NodeID = strings.TrimSpace(item.NodeID)
	item.ServiceName = strings.TrimSpace(item.ServiceName)
	item.ServiceKind = strings.TrimSpace(item.ServiceKind)
	item.Protocol = strings.TrimSpace(item.Protocol)
	item.Host = strings.TrimSpace(item.Host)
	item.GatewayPath = strings.TrimSpace(item.GatewayPath)
	item.GatewayServiceID = strings.TrimSpace(item.GatewayServiceID)
	item.Scope = strings.TrimSpace(item.Scope)
	item.Status = strings.TrimSpace(item.Status)
	item.Description = strings.TrimSpace(item.Description)
	item.ExtraConfig = strings.TrimSpace(item.ExtraConfig)
	if item.ServiceKind == "" {
		item.ServiceKind = "service"
	}
	if item.Protocol == "" {
		item.Protocol = "http"
	}
	if item.Scope == "" {
		item.Scope = "public"
	}
	if item.Status == "" {
		item.Status = "active"
	}
	if item.ExtraConfig == "" {
		item.ExtraConfig = "{}"
	}
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint")
}
