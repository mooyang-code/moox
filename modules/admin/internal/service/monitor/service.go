package monitor

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/service/monitor/model"
)

// Service 监控服务接口
type Service interface {
	// ========== 监控数据查询 ==========

	// GetCurrentMetrics 获取指定主机的当前监控指标
	// hostIDs 为空时返回所有启用监控的主机
	GetCurrentMetrics(ctx context.Context, hostIDs []int) ([]*model.HostMetrics, error)

	// GetHistoryMetrics 获取主机历史监控数据
	// duration: 时间范围（如 "1h", "24h", "7d"）
	GetHistoryMetrics(ctx context.Context, hostAddress string, duration string) ([]*model.HistoryPoint, error)

	// ========== 内部方法（供定时器调用） ==========

	// CollectAll 执行一次完整采集（由定时器调用）
	CollectAll(ctx context.Context) error

	// CleanHistory 清理历史监控数据（由定时器调用）
	CleanHistory(ctx context.Context, keepDays int) error
}
