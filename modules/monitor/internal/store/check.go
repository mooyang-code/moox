package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
)

type CheckRepository struct {
	db *gorm.DB
}

type ListChecksOptions struct {
	SpaceID   string
	GroupName string
	Source    string
	Enabled   *bool
	Page      Page
}

func NewCheckRepository(db *gorm.DB) *CheckRepository {
	return &CheckRepository{db: db}
}

func (r *CheckRepository) Create(ctx context.Context, check *domain.Check) error {
	return r.db.WithContext(ctx).Create(check).Error
}

func (r *CheckRepository) Get(ctx context.Context, spaceID, checkID string) (*domain.Check, error) {
	var check domain.Check
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_check_id = ?", spaceID, checkID).
		First(&check).Error
	if err != nil {
		return nil, err
	}
	return &check, nil
}

func (r *CheckRepository) GetByCheckID(ctx context.Context, checkID string) (*domain.Check, error) {
	var check domain.Check
	err := r.db.WithContext(ctx).Where("c_check_id = ?", checkID).First(&check).Error
	if err != nil {
		return nil, err
	}
	return &check, nil
}

func (r *CheckRepository) Update(ctx context.Context, check *domain.Check) error {
	return r.db.WithContext(ctx).
		Model(&domain.Check{}).
		Where("c_space_id = ? AND c_check_id = ?", check.SpaceID, check.CheckID).
		Updates(map[string]any{
			"c_name":             check.Name,
			"c_group_name":       check.GroupName,
			"c_kind":             check.Kind,
			"c_url":              check.URL,
			"c_method":           check.Method,
			"c_headers":          check.Headers,
			"c_body":             check.Body,
			"c_tcp_host":         check.TCPHost,
			"c_tcp_port":         check.TCPPort,
			"c_interval_seconds": check.IntervalSeconds,
			"c_timeout_ms":       check.TimeoutMS,
			"c_expected_status":  check.ExpectedStatus,
			"c_max_response_ms":  check.MaxResponseMS,
			"c_body_contains":    check.BodyContains,
			"c_enabled":          check.Enabled,
			"c_source":           check.Source,
			"c_labels":           check.Labels,
			"c_description":      check.Description,
			"c_last_checked_at":  check.LastCheckedAt,
			"c_next_check_at":    check.NextCheckAt,
		}).Error
}

// UpdatePlacementDefinition 用部署生成的定义整体覆盖部署检查；部署检查没有手工启停。
func (r *CheckRepository) UpdatePlacementDefinition(ctx context.Context, check *domain.Check) error {
	return r.db.WithContext(ctx).
		Model(&domain.Check{}).
		Where("c_space_id = ? AND c_check_id = ? AND c_source = ?", check.SpaceID, check.CheckID, domain.CheckSourcePlacement).
		Updates(map[string]any{
			"c_name":             check.Name,
			"c_group_name":       check.GroupName,
			"c_kind":             check.Kind,
			"c_url":              check.URL,
			"c_method":           check.Method,
			"c_headers":          check.Headers,
			"c_body":             check.Body,
			"c_tcp_host":         check.TCPHost,
			"c_tcp_port":         check.TCPPort,
			"c_interval_seconds": check.IntervalSeconds,
			"c_timeout_ms":       check.TimeoutMS,
			"c_expected_status":  check.ExpectedStatus,
			"c_max_response_ms":  check.MaxResponseMS,
			"c_body_contains":    check.BodyContains,
			"c_enabled":          check.Enabled,
			"c_source":           check.Source,
			"c_labels":           check.Labels,
			"c_description":      check.Description,
		}).Error
}

// DeleteWithRules 删除检查及其结果、告警规则和告警状态；告警事件作为历史保留，按保留期清理。
func (r *CheckRepository) DeleteWithRules(ctx context.Context, spaceID, checkID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		where := "c_space_id = ? AND c_check_id = ?"
		for _, model := range []any{&domain.AlertState{}, &domain.AlertRule{}, &domain.CheckResult{}, &domain.Check{}} {
			if err := tx.Where(where, spaceID, checkID).Delete(model).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListBySource 返回某个来源的全部检查（所有空间）。
func (r *CheckRepository) ListBySource(ctx context.Context, source string) ([]domain.Check, error) {
	var checks []domain.Check
	err := r.db.WithContext(ctx).Where("c_source = ?", source).Order("c_check_id ASC").Find(&checks).Error
	return checks, err
}

func (r *CheckRepository) Delete(ctx context.Context, spaceID, checkID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var references int64
		if err := tx.Model(&domain.AlertRule{}).
			Where("c_space_id = ? AND c_check_id = ?", spaceID, checkID).
			Count(&references).Error; err != nil {
			return err
		}
		if references > 0 {
			return fmt.Errorf("%w: check %q is bound to %d alert rule(s)", ErrResourceReferenced, checkID, references)
		}
		if err := tx.Where("c_space_id = ? AND c_check_id = ?", spaceID, checkID).
			Delete(&domain.CheckResult{}).Error; err != nil {
			return err
		}
		deleted := tx.Where("c_space_id = ? AND c_check_id = ?", spaceID, checkID).
			Delete(&domain.Check{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (r *CheckRepository) List(ctx context.Context, opts ListChecksOptions) ([]domain.Check, error) {
	q := r.applyFilters(r.db.WithContext(ctx), opts)
	var checks []domain.Check
	err := q.Order("c_group_name ASC, c_name ASC").
		Limit(opts.Page.limit()).
		Offset(opts.Page.offset()).
		Find(&checks).Error
	return checks, err
}

func (r *CheckRepository) Count(ctx context.Context, opts ListChecksOptions) (int64, error) {
	var total int64
	err := r.applyFilters(r.db.WithContext(ctx).Model(&domain.Check{}), opts).Count(&total).Error
	return total, err
}

func (r *CheckRepository) CountEnabled(ctx context.Context) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&domain.Check{}).
		Where("c_enabled = 1").Count(&total).Error
	return total, err
}

// IsPlacementRegistered 判断组件在这台主机上是否有启用的部署检查，即是否已在 SysDeploy 登记并启用。
func (r *CheckRepository) IsPlacementRegistered(ctx context.Context, componentID, hostID string) (bool, error) {
	if r == nil || r.db == nil {
		return false, gorm.ErrInvalidDB
	}
	var count int64
	checkID := "placement:" + strings.TrimSpace(hostID) + ":" + strings.TrimSpace(componentID)
	err := r.db.WithContext(ctx).Model(&domain.Check{}).
		Where("c_check_id = ? AND c_source = ? AND c_enabled = 1", checkID, domain.CheckSourcePlacement).
		Count(&count).Error
	return count > 0, err
}

func (r *CheckRepository) applyFilters(q *gorm.DB, opts ListChecksOptions) *gorm.DB {
	if opts.SpaceID != "" {
		q = q.Where("c_space_id = ?", opts.SpaceID)
	}
	if opts.GroupName != "" {
		q = q.Where("c_group_name = ?", opts.GroupName)
	}
	if opts.Source != "" {
		q = q.Where("c_source = ?", opts.Source)
	}
	if opts.Enabled != nil {
		q = q.Where("c_enabled = ?", *opts.Enabled)
	}
	return q
}

func (r *CheckRepository) ListDue(ctx context.Context, now time.Time, limit int) ([]domain.Check, error) {
	if limit <= 0 {
		limit = 100
	}
	var candidates []domain.Check
	err := r.db.WithContext(ctx).
		Where("c_enabled = 1 AND c_kind IN ? AND c_source IN ?", []string{domain.CheckKindHTTP, domain.CheckKindTCP}, []string{domain.CheckSourcePlacement, domain.CheckSourceObservability}).
		Order("c_next_check_at ASC, c_id ASC").
		Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	checks := make([]domain.Check, 0, min(limit, len(candidates)))
	for _, check := range candidates {
		if check.NextCheckAt == nil || !check.NextCheckAt.After(now) {
			checks = append(checks, check)
			if len(checks) >= limit {
				break
			}
		}
	}
	return checks, nil
}

func (r *CheckRepository) MarkChecked(ctx context.Context, spaceID, checkID string, checkedAt, nextAt time.Time) error {
	return r.db.WithContext(ctx).
		Model(&domain.Check{}).
		Where("c_space_id = ? AND c_check_id = ?", spaceID, checkID).
		Updates(map[string]any{
			"c_last_checked_at": checkedAt,
			"c_next_check_at":   nextAt,
		}).Error
}
