package dao

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/control/internal/service/monitor/model"
	"gorm.io/gorm"
)

type MonitorHistoryDAO struct {
	db *gorm.DB
}

func NewMonitorHistoryDAO(db *gorm.DB) *MonitorHistoryDAO {
	return &MonitorHistoryDAO{db: db}
}

// Insert 插入历史记录
func (d *MonitorHistoryDAO) Insert(ctx context.Context, history *model.MonitorHistory) error {
	return d.db.WithContext(ctx).Create(history).Error
}

// Query 查询历史数据
// duration: 时间范围，如 "1h", "24h", "7d"
func (d *MonitorHistoryDAO) Query(ctx context.Context, hostAddress string, duration string) ([]*model.MonitorHistory, error) {
	// 解析 duration 并转换为 SQLite 时间函数
	var timeExpr string
	switch duration {
	case "1h":
		timeExpr = "datetime('now', '-1 hour')"
	case "24h":
		timeExpr = "datetime('now', '-24 hour')"
	case "7d":
		timeExpr = "datetime('now', '-7 day')"
	default:
		timeExpr = "datetime('now', '-1 hour')"
	}

	var results []*model.MonitorHistory
	err := d.db.WithContext(ctx).
		Table("t_host_monitor_history").
		Where("c_host_address = ?", hostAddress).
		Where(fmt.Sprintf("c_collect_time >= %s", timeExpr)).
		Order("c_collect_time ASC").
		Find(&results).Error

	return results, err
}

// CleanOldData 清理旧数据（保留最近 N 天）
func (d *MonitorHistoryDAO) CleanOldData(ctx context.Context, keepDays int) (int64, error) {
	timeExpr := fmt.Sprintf("datetime('now', '-%d day')", keepDays)
	result := d.db.WithContext(ctx).
		Table("t_host_monitor_history").
		Where(fmt.Sprintf("c_collect_time < %s", timeExpr)).
		Delete(nil)

	if result.Error != nil {
		return 0, result.Error
	}

	return result.RowsAffected, nil
}
