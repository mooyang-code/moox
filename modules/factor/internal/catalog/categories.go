package catalog

import (
	"context"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

// ListCategories 返回全部因子分类。
func (s *Service) ListCategories(ctx context.Context) ([]domain.FactorCategory, error) {
	return s.db.ListCategories(ctx)
}

// UpsertCategory 新建分类，或修改已有分类的名称与顺序。
func (s *Service) UpsertCategory(ctx context.Context, category domain.FactorCategory) (domain.FactorCategory, error) {
	return s.db.UpsertCategory(ctx, category)
}

// DeleteCategory 删除没有定义使用的分类。
func (s *Service) DeleteCategory(ctx context.Context, categoryID string) error {
	return s.db.DeleteCategory(ctx, categoryID)
}

// SetFactorCategory 修改定义的分类：分类是元数据，不进 definition_hash，成员启用期间也可以改。
func (s *Service) SetFactorCategory(ctx context.Context, factorID, categoryID string) (domain.FactorDef, error) {
	factorID, categoryID = strings.TrimSpace(factorID), strings.TrimSpace(categoryID)
	if categoryID == "" {
		categoryID = domain.DefaultCategoryID
	}
	unlock, err := s.locks.LockFactorContext(ctx, factorID)
	if err != nil {
		return domain.FactorDef{}, err
	}
	defer unlock()
	if err := s.db.SetFactorCategory(ctx, factorID, categoryID); err != nil {
		return domain.FactorDef{}, err
	}
	return s.db.GetFactor(ctx, factorID)
}
