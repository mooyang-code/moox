package store

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// SeedKey 是一个正在历史补齐的（空间、数据集、频率）。
type SeedKey struct {
	SpaceID   string
	DatasetID string
	Freq      string
}

// ReplaceHistorySeeds 用最近一次从 Collector 取到的补齐中集合整表替换已保存的集合。
func (s *Store) ReplaceHistorySeeds(ctx context.Context, keys []SeedKey) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM t_factor_history_seeds`).Error; err != nil {
			return err
		}
		for _, key := range keys {
			if strings.TrimSpace(key.SpaceID) == "" || strings.TrimSpace(key.DatasetID) == "" || strings.TrimSpace(key.Freq) == "" {
				continue
			}
			if err := tx.Exec(`INSERT OR IGNORE INTO t_factor_history_seeds (c_space_id, c_dataset_id, c_freq) VALUES (?, ?, ?)`,
				key.SpaceID, key.DatasetID, key.Freq).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListHistorySeeds 返回已保存的补齐中集合；从没保存过时为空。
func (s *Store) ListHistorySeeds(ctx context.Context) ([]SeedKey, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	var rows []struct {
		SpaceID   string `gorm:"column:c_space_id"`
		DatasetID string `gorm:"column:c_dataset_id"`
		Freq      string `gorm:"column:c_freq"`
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT c_space_id, c_dataset_id, c_freq FROM t_factor_history_seeds ORDER BY c_space_id, c_dataset_id, c_freq`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]SeedKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, SeedKey{SpaceID: r.SpaceID, DatasetID: r.DatasetID, Freq: r.Freq})
	}
	return out, nil
}
