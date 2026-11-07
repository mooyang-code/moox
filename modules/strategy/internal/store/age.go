package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// SQL 整条写成常量，不在调用处拼接。
const (
	sqlLoadAgePassed = `
		SELECT c_subject_id FROM t_strategy_age_passed
		WHERE c_space_id = ? AND c_view_id = ? AND c_index_id = ? AND c_min_age_bars = ? AND c_market = ?`
	sqlDeleteStaleAgePassed = `DELETE FROM t_strategy_age_passed WHERE c_space_id = ? AND c_view_id = ? AND c_index_id <> ?`
	sqlInsertAgePassed      = `INSERT OR IGNORE INTO t_strategy_age_passed (c_space_id, c_view_id, c_index_id, c_min_age_bars, c_market, c_subject_id, c_ctime) VALUES (?, ?, ?, ?, ?, ?, ?)`
)

// LoadAgePassed 读出某个 View 活动索引下、某个 min_age_bars、某个市场已通过年龄探针的标的。
func (s *Store) LoadAgePassed(ctx context.Context, spaceID, viewID, indexID string, minAgeBars int, market string) ([]string, error) {
	var ids []string
	err := s.db.WithContext(ctx).Raw(sqlLoadAgePassed, spaceID, viewID, indexID, minAgeBars, market).Scan(&ids).Error
	return ids, err
}

// SaveAgePassed 登记通过年龄探针的标的，并清掉该 View 其他活动索引下的旧记录（索引重建后不再有效）。
func (s *Store) SaveAgePassed(ctx context.Context, spaceID, viewID, indexID string, minAgeBars int, market string, subjectIDs []string, at time.Time) error {
	if len(subjectIDs) == 0 {
		return nil
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec(sqlDeleteStaleAgePassed, spaceID, viewID, indexID).Error; err != nil {
			return err
		}
		for _, id := range subjectIDs {
			if err := tx.Exec(sqlInsertAgePassed, spaceID, viewID, indexID, minAgeBars, market, id, at.UTC()).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
