package repository

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

func (r *CatalogRepository) FindNodeForInvocation(ctx context.Context, spaceID string, deploymentID string, workloadType string) (*CloudNode, error) {
	if strings.TrimSpace(spaceID) == "" {
		return nil, fmt.Errorf("space_id is required")
	}
	q := r.db.WithContext(ctx).Where("c_is_deleted = ? AND c_status != ? AND c_space_id = ?", false, "deleted", spaceID)
	if deploymentID != "" {
		q = q.Where("c_deployment_id = ?", deploymentID)
	}
	if workloadType != "" {
		q = q.Where("c_supported_workloads LIKE ? OR c_metadata LIKE ?", "%"+workloadType+"%", "%"+workloadType+"%")
	}
	var node CloudNode
	if err := q.Order("c_last_heartbeat_at DESC, c_id DESC").First(&node).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &node, nil
}

func (r *CatalogRepository) SaveInvocation(ctx context.Context, summary Invocation, details []InvocationResult) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&summary).Error; err != nil {
			return err
		}
		if len(details) > 0 {
			return tx.Create(&details).Error
		}
		return nil
	})
}
