package store

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ageSaveBatch 是每条插入语句的行数：参数个数有界，也避开大 VALUES 列表的解析开销。
const ageSaveBatch = 50

// LoadAgePassed 读出某个 View 活动索引下、某个 min_age_bars 已通过年龄探针的标的。
func (s *Store) LoadAgePassed(ctx context.Context, spaceID, viewID, indexID string, minAgeBars int) ([]string, error) {
	var ids []string
	err := s.db.WithContext(ctx).Raw(`
		SELECT c_subject_id FROM t_strategy_age_passed
		WHERE c_space_id = ? AND c_view_id = ? AND c_index_id = ? AND c_min_age_bars = ?
	`, spaceID, viewID, indexID, minAgeBars).Scan(&ids).Error
	return ids, err
}

// SaveAgePassed 登记通过年龄探针的标的，并清掉该 View 其他活动索引下的旧记录（索引重建后不再有效）。
func (s *Store) SaveAgePassed(ctx context.Context, spaceID, viewID, indexID string, minAgeBars int, subjectIDs []string, at time.Time) error {
	if len(subjectIDs) == 0 {
		return nil
	}
	return s.transaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM t_strategy_age_passed WHERE c_space_id = ? AND c_view_id = ? AND c_index_id <> ?`, spaceID, viewID, indexID).Error; err != nil {
			return err
		}
		for start := 0; start < len(subjectIDs); start += ageSaveBatch {
			chunk := subjectIDs[start:min(start+ageSaveBatch, len(subjectIDs))]
			args := make([]any, 0, len(chunk)*6)
			for _, id := range chunk {
				args = append(args, spaceID, viewID, indexID, minAgeBars, id, at.UTC())
			}
			values := strings.TrimRight(strings.Repeat("(?, ?, ?, ?, ?, ?),", len(chunk)), ",")
			if err := tx.Exec(`INSERT OR IGNORE INTO t_strategy_age_passed (c_space_id, c_view_id, c_index_id, c_min_age_bars, c_subject_id, c_ctime) VALUES `+values, args...).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
