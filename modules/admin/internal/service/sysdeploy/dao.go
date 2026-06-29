package sysdeploy

import (
	"context"
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
	if item.ServiceName == "" {
		return fmt.Errorf("service_name is required")
	}
	if exists, err := d.exists(ctx, item.ServiceName); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("service deployment already exists: %s", item.ServiceName)
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

func (d *DAO) Update(ctx context.Context, serviceName string, item *Deployment) error {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return fmt.Errorf("service_name is required")
	}
	if item == nil {
		return fmt.Errorf("deployment is required")
	}
	item.ServiceName = serviceName
	normalizeDeployment(item)
	result := d.db.WithContext(ctx).Model(&Deployment{}).
		Where("c_service_name = ? AND c_is_deleted != 'true'", serviceName).
		Updates(map[string]interface{}{
			"c_service_kind": item.ServiceKind,
			"c_protocol":     item.Protocol,
			"c_host":         item.Host,
			"c_port":         item.Port,
			"c_base_url":     item.BaseURL,
			"c_rpc_address":  item.RPCAddress,
			"c_gateway_path": item.GatewayPath,
			"c_scope":        item.Scope,
			"c_status":       item.Status,
			"c_description":  item.Description,
			"c_extra_config": item.ExtraConfig,
			"c_mtime":        time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("service deployment not found: %s", serviceName)
	}
	return nil
}

func (d *DAO) Delete(ctx context.Context, serviceName string) error {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return fmt.Errorf("service_name is required")
	}
	result := d.db.WithContext(ctx).Model(&Deployment{}).
		Where("c_service_name = ? AND c_is_deleted != 'true'", serviceName).
		Updates(map[string]interface{}{
			"c_is_deleted": "true",
			"c_mtime":      time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("service deployment not found: %s", serviceName)
	}
	return nil
}

func (d *DAO) Get(ctx context.Context, serviceName string) (*Deployment, error) {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return nil, fmt.Errorf("service_name is required")
	}
	var row Deployment
	err := d.db.WithContext(ctx).Where("c_service_name = ? AND c_is_deleted != 'true'", serviceName).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (d *DAO) List(ctx context.Context, filter ListFilter, offset int, limit int) ([]Deployment, int64, error) {
	query := d.db.WithContext(ctx).Model(&Deployment{}).Where("c_is_deleted != 'true'")
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
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []Deployment
	if err := query.Order("c_scope DESC, c_service_kind ASC, c_service_name ASC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (d *DAO) ListActive(ctx context.Context) ([]Deployment, error) {
	var rows []Deployment
	err := d.db.WithContext(ctx).
		Where("c_is_deleted != 'true' AND c_status = 'active'").
		Order("c_scope DESC, c_service_kind ASC, c_service_name ASC").
		Find(&rows).Error
	return rows, err
}

func (d *DAO) SeedDefaults(ctx context.Context, rows []Deployment) error {
	for i := range rows {
		item := rows[i]
		normalizeDeployment(&item)
		if item.ServiceName == "" {
			continue
		}
		exists, err := d.exists(ctx, item.ServiceName)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if err := d.Create(ctx, &item); err != nil {
			return err
		}
	}
	return nil
}

func (d *DAO) exists(ctx context.Context, serviceName string) (bool, error) {
	var count int64
	err := d.db.WithContext(ctx).Model(&Deployment{}).
		Where("c_service_name = ? AND c_is_deleted != 'true'", serviceName).
		Count(&count).Error
	return count > 0, err
}

type ListFilter struct {
	ServiceName string
	ServiceKind string
	Scope       string
	Status      string
}

func normalizeDeployment(item *Deployment) {
	item.ServiceName = strings.TrimSpace(item.ServiceName)
	item.ServiceKind = strings.TrimSpace(item.ServiceKind)
	item.Protocol = strings.TrimSpace(item.Protocol)
	item.Host = strings.TrimSpace(item.Host)
	item.BaseURL = strings.TrimRight(strings.TrimSpace(item.BaseURL), "/")
	item.RPCAddress = strings.TrimSpace(item.RPCAddress)
	item.GatewayPath = strings.TrimSpace(item.GatewayPath)
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
	if item.RPCAddress == "" && item.Host != "" && item.Port > 0 {
		item.RPCAddress = fmt.Sprintf("%s:%d", item.Host, item.Port)
	}
	if item.BaseURL == "" && item.Protocol != "" && item.Host != "" && item.Port > 0 {
		item.BaseURL = fmt.Sprintf("%s://%s:%d", item.Protocol, item.Host, item.Port)
	}
	if item.IsDeleted == "" {
		item.IsDeleted = "false"
	}
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint") || strings.Contains(message, "constraint failed")
}
