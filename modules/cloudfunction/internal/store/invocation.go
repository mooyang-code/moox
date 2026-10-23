package store

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

func (r *CatalogRepository) FindNodeForInvocation(ctx context.Context, spaceID string, deploymentID string, _ string) (*CloudNode, error) {
	if strings.TrimSpace(spaceID) == "" {
		return nil, fmt.Errorf("space_id is required")
	}
	q := r.db.WithContext(ctx).Where("c_is_deleted = ? AND c_space_id = ?", false, spaceID).
		Where("CASE WHEN json_valid(c_metadata) THEN COALESCE(json_extract(c_metadata, '$.biz_type'), '') ELSE '' END <> ?", "market_fetcher")
	if deploymentID != "" {
		q = q.Where("c_deployment_id = ?", deploymentID)
	}
	var node CloudNode
	if err := q.Order("c_id DESC").First(&node).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &node, nil
}
