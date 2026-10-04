package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
)

func (r *TaskRepository) ReplaceTaskSeries(ctx context.Context, spaceID, taskID string, input []domain.TaskSeries) (string, bool, error) {
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	if spaceID == "" || taskID == "" {
		return "", false, fmt.Errorf("space_id and task_id are required")
	}
	byKey := make(map[string]domain.TaskSeries, len(input))
	for _, item := range input {
		item.SpaceID, item.TaskID = spaceID, taskID
		item.Provider = strings.ToLower(strings.TrimSpace(item.Provider))
		item.SourceID = strings.ToLower(strings.TrimSpace(item.SourceID))
		item.MarketType = strings.ToLower(strings.TrimSpace(item.MarketType))
		item.SubjectID = strings.ToUpper(strings.TrimSpace(item.SubjectID))
		item.ProviderSymbol = strings.TrimSpace(item.ProviderSymbol)
		item.SeriesTag = strings.TrimSpace(item.SeriesTag)
		item.SeriesKey = domain.CanonicalSeriesKey(item.Provider, item.SourceID, item.MarketType, item.SubjectID, item.SeriesTag)
		if item.SubjectID == "" || item.SeriesKey == "" {
			continue
		}
		byKey[item.SeriesKey] = item
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "", false, fmt.Errorf("task series set must not be empty")
	}
	hash := domain.SeriesSetHash(keys)
	now := time.Now().UTC()
	changed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current string
		if err := tx.Model(&domain.CollectionTask{}).Select("c_series_hash").Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).Scan(&current).Error; err != nil {
			return err
		}
		if strings.TrimSpace(current) == hash {
			return nil
		}
		if err := tx.Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).Delete(&domain.TaskSeries{}).Error; err != nil {
			return err
		}
		rows := make([]domain.TaskSeries, 0, len(keys))
		for index, key := range keys {
			item := byKey[key]
			item.SeriesIndex = uint32(index)
			item.CreateTime, item.ModifyTime = now, now
			rows = append(rows, item)
		}
		if err := tx.CreateInBatches(&rows, 500).Error; err != nil {
			return err
		}
		result := tx.Model(&domain.CollectionTask{}).Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).Updates(map[string]any{"c_series_hash": hash, "c_mtime": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		changed = true
		return nil
	})
	return hash, changed, err
}

func (r *TaskRepository) ListTaskSeries(ctx context.Context, spaceID, taskID string) ([]domain.TaskSeries, error) {
	var rows []domain.TaskSeries
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_task_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(taskID)).Order("c_series_index ASC").Find(&rows).Error
	return rows, err
}

// ReadSingleTaskSeries materializes at most two rows, enough to prove whether
// a task has exactly one current series without scanning a potentially large
// task roster. Callers must still verify the task's stored SeriesHash.
func (r *TaskRepository) ReadSingleTaskSeries(ctx context.Context, spaceID, taskID string) (*domain.TaskSeries, bool, error) {
	var rows []domain.TaskSeries
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_task_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(taskID)).
		Order("c_series_index ASC").Limit(2).Find(&rows).Error
	if err != nil {
		return nil, false, err
	}
	if len(rows) != 1 {
		return nil, false, nil
	}
	return &rows[0], true, nil
}

// InvalidateTaskSeries fails closed when the current Tag union cannot be
// materialized. Keeping an older series_hash would let a scheduler silently
// plan stale membership after a routing/tag error.
func (r *TaskRepository) InvalidateTaskSeries(ctx context.Context, spaceID, taskID string) error {
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	if spaceID == "" || taskID == "" {
		return fmt.Errorf("space_id and task_id are required")
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).Delete(&domain.TaskSeries{}).Error; err != nil {
			return err
		}
		result := tx.Model(&domain.CollectionTask{}).Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).Updates(map[string]any{"c_series_hash": "", "c_mtime": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}
