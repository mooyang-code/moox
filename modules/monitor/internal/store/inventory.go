package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// InventoryRepository 保存最后一份有效的采集任务清单（只有一行）。
type InventoryRepository struct{ store *Store }

type inventoryRow struct {
	ID        int       `gorm:"column:c_id;primaryKey"`
	Snapshot  string    `gorm:"column:c_snapshot"`
	FetchedAt time.Time `gorm:"column:c_fetched_at"`
}

func (inventoryRow) TableName() string { return "t_monitor_task_inventory" }

// NewInventoryRepository 创建清单仓储。
func NewInventoryRepository(store *Store) *InventoryRepository {
	return &InventoryRepository{store: store}
}

// SaveInventory 覆盖保存清单。
func (r *InventoryRepository) SaveInventory(ctx context.Context, snapshot []byte, fetchedAt time.Time) error {
	_, err := WithDatabase(r.store, func(db *gorm.DB) error {
		return db.WithContext(ctx).Exec(`INSERT INTO t_monitor_task_inventory (c_id, c_snapshot, c_fetched_at, c_mtime) VALUES (1, ?, ?, CURRENT_TIMESTAMP)
			ON CONFLICT (c_id) DO UPDATE SET c_snapshot = excluded.c_snapshot, c_fetched_at = excluded.c_fetched_at, c_mtime = CURRENT_TIMESTAMP`, string(snapshot), fetchedAt.UTC()).Error
	})
	return err
}

// LoadInventory 读取清单；没有则返回空。
func (r *InventoryRepository) LoadInventory(ctx context.Context) ([]byte, time.Time, error) {
	type result struct {
		raw       []byte
		fetchedAt time.Time
	}
	value, err := WithDatabase(r.store, func(db *gorm.DB) result {
		var row inventoryRow
		if err := db.WithContext(ctx).Take(&row).Error; err != nil {
			return result{}
		}
		return result{raw: []byte(row.Snapshot), fetchedAt: row.FetchedAt}
	})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("load inventory: %w", err)
	}
	return value.raw, value.fetchedAt, nil
}
