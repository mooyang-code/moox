package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

// ListCategories returns every factor category by sort order and id.
func (s *Store) ListCategories(ctx context.Context) ([]domain.FactorCategory, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	var categories []domain.FactorCategory
	if err := s.db.WithContext(ctx).Raw(`
		SELECT c_category_id, c_name, c_sort_order, c_ctime, c_mtime FROM t_factor_categories ORDER BY c_sort_order, c_category_id
	`).Scan(&categories).Error; err != nil {
		return nil, err
	}
	return categories, nil
}

// EnsureDefaultCategory 补回默认分类“未分类”（建表脚本也会写入；重置清空分类后由这里立刻补回）。
func (s *Store) EnsureDefaultCategory(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	return s.db.WithContext(ctx).Exec(`
		INSERT OR IGNORE INTO t_factor_categories (c_category_id, c_name, c_sort_order) VALUES (?, '未分类', 1000)
	`, domain.DefaultCategoryID).Error
}

// UpsertCategory creates a category or renames and reorders an existing one.
func (s *Store) UpsertCategory(ctx context.Context, category domain.FactorCategory) (domain.FactorCategory, error) {
	if s == nil || s.db == nil {
		return domain.FactorCategory{}, fmt.Errorf("factor database is not open")
	}
	category.CategoryID, category.Name = strings.TrimSpace(category.CategoryID), strings.TrimSpace(category.Name)
	if err := domain.ValidateCategory(category); err != nil {
		return domain.FactorCategory{}, err
	}
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Exec(`
		INSERT INTO t_factor_categories (c_category_id, c_name, c_sort_order, c_ctime, c_mtime) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (c_category_id) DO UPDATE SET c_name = excluded.c_name, c_sort_order = excluded.c_sort_order, c_mtime = excluded.c_mtime
	`, category.CategoryID, category.Name, category.SortOrder, now, now).Error; err != nil {
		return domain.FactorCategory{}, err
	}
	var stored domain.FactorCategory
	if err := s.db.WithContext(ctx).Raw(`
		SELECT c_category_id, c_name, c_sort_order, c_ctime, c_mtime FROM t_factor_categories WHERE c_category_id = ?
	`, category.CategoryID).Scan(&stored).Error; err != nil {
		return domain.FactorCategory{}, err
	}
	return stored, nil
}

// DeleteCategory removes a category that no definition uses; the default category cannot be removed.
func (s *Store) DeleteCategory(ctx context.Context, categoryID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	categoryID = strings.TrimSpace(categoryID)
	if categoryID == domain.DefaultCategoryID {
		return fmt.Errorf("%w: the default category cannot be deleted", ErrConflict)
	}
	result := s.db.WithContext(ctx).Exec("DELETE FROM t_factor_categories WHERE c_category_id = ?", categoryID)
	if result.Error != nil {
		if isForeignKeyConstraint(result.Error) {
			return fmt.Errorf("%w: category %q is still used by factor definitions", ErrConflict, categoryID)
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
