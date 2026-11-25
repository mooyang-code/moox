package store

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MaxChecks 是检查表的行数上限：覆盖 1024 台主机各自的主机、组件检查（每台数条）及业务检查。超过时本轮报错、不写。
const MaxChecks = 1024 * 8

type CheckRepository struct {
	db *gorm.DB
}

func NewCheckRepository(db *gorm.DB) *CheckRepository {
	return &CheckRepository{db: db}
}

// CheckKey 是一项检查的身份。
type CheckKey struct{ SpaceID, CheckID string }

// KeyOf 返回检查行的身份。
func KeyOf(check domain.Check) CheckKey {
	return CheckKey{SpaceID: check.SpaceID, CheckID: check.CheckID}
}

// ListAll 读出全部检查行；超过 MaxChecks 时报错。
func (r *CheckRepository) ListAll(ctx context.Context) ([]domain.Check, error) {
	var checks []domain.Check
	if err := r.db.WithContext(ctx).Order("c_space_id, c_check_id").Limit(MaxChecks + 1).Find(&checks).Error; err != nil {
		return nil, err
	}
	if len(checks) > MaxChecks {
		return nil, fmt.Errorf("监控检查超过上限 %d 项", MaxChecks)
	}
	return checks, nil
}

// ListFiring 返回正在告警的检查，按触发时间倒序。spaceID 不为空时只取本空间与系统空间（mooxsys）的。
func (r *CheckRepository) ListFiring(ctx context.Context, spaceID string, limit int) ([]domain.Check, error) {
	query := r.db.WithContext(ctx).Where("c_alert_status = ?", domain.AlertStatusFiring)
	if spaceID != "" {
		query = query.Where("c_space_id IN ?", []string{spaceID, "", events.SystemSpaceID})
	}
	query = query.Order("c_triggered_at DESC, c_space_id, c_check_id")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var checks []domain.Check
	err := query.Find(&checks).Error
	return checks, err
}

// List 返回检查行；spaceID 不为空时只取本空间与系统空间的。
func (r *CheckRepository) List(ctx context.Context, spaceID string) ([]domain.Check, error) {
	query := r.db.WithContext(ctx)
	if spaceID != "" {
		query = query.Where("c_space_id IN ?", []string{spaceID, "", events.SystemSpaceID})
	}
	var checks []domain.Check
	if err := query.Order("c_space_id, c_check_id").Limit(MaxChecks + 1).Find(&checks).Error; err != nil {
		return nil, err
	}
	if len(checks) > MaxChecks {
		return nil, fmt.Errorf("监控检查超过上限 %d 项", MaxChecks)
	}
	return checks, nil
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

func (r *CheckRepository) Count(ctx context.Context) (int64, error) {
	var total int64
	err := r.db.WithContext(ctx).Model(&domain.Check{}).Count(&total).Error
	return total, err
}

// checkColumns 是更新已有检查行时覆盖的列（c_ctime 保留首次出现的时间）。
var checkColumns = []string{
	"c_name", "c_severity", "c_status", "c_reason", "c_raw_error", "c_checked_at",
	"c_failure_reason", "c_failure_raw_error", "c_failed_at", "c_failure_count", "c_success_count",
	"c_alert_status", "c_triggered_at", "c_notified_at",
}

// SaveRound 在一个事务里写入一轮的结果：更新或新建检查行、删除不再产出的行、写告警事件。
func (r *CheckRepository) SaveRound(ctx context.Context, upserts []domain.Check, deletes []CheckKey, alertEvents []domain.AlertEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index := range upserts {
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "c_space_id"}, {Name: "c_check_id"}},
				DoUpdates: clause.AssignmentColumns(checkColumns),
			}).Create(&upserts[index]).Error; err != nil {
				return err
			}
		}
		for _, key := range deletes {
			if err := tx.Where("c_space_id = ? AND c_check_id = ?", key.SpaceID, key.CheckID).Delete(&domain.Check{}).Error; err != nil {
				return err
			}
		}
		for index := range alertEvents {
			if err := tx.Create(&alertEvents[index]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
