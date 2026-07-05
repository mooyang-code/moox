package dao

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"gorm.io/gorm/clause"
)

// GetSyncCursor 查询单个定时同步游标。
func (g *GormStore) GetSyncCursor(ctx context.Context, spaceID, accountID string, syncType service.SyncType, symbol string) (*service.SyncCursor, error) {
	var out service.SyncCursor
	res := g.db.WithContext(ctx).
		Where("c_space_id = ? AND c_account_id = ? AND c_sync_type = ? AND c_symbol = ?", spaceID, accountID, syncType, symbol).
		Limit(1).
		Find(&out)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, service.ErrNotFound
	}
	return &out, nil
}

// UpsertSyncCursor 按 space/account/type/symbol 幂等写入定时同步游标。
func (g *GormStore) UpsertSyncCursor(ctx context.Context, spaceID string, cursor *service.SyncCursor) error {
	if cursor == nil || cursor.AccountID == "" || cursor.SyncType == "" {
		return service.ErrInvalidParam
	}
	cursor.SpaceID = spaceID
	return g.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "c_space_id"},
			{Name: "c_account_id"},
			{Name: "c_sync_type"},
			{Name: "c_symbol"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_channel_id",
			"c_exchange",
			"c_market_type",
			"c_cursor_start_ms",
			"c_cursor_end_ms",
			"c_last_success_at",
			"c_last_error",
			"c_is_enabled",
		}),
	}).Create(cursor).Error
}

// ListSyncCursors 查询定时同步游标列表。
func (g *GormStore) ListSyncCursors(ctx context.Context, spaceID, accountID string, syncType service.SyncType) ([]*service.SyncCursor, error) {
	q := g.db.WithContext(ctx).Model(&service.SyncCursor{}).Where("c_space_id = ?", spaceID)
	if accountID != "" {
		q = q.Where("c_account_id = ?", accountID)
	}
	if syncType != "" {
		q = q.Where("c_sync_type = ?", syncType)
	}
	var out []*service.SyncCursor
	if err := q.Order("c_account_id ASC, c_sync_type ASC, c_symbol ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
