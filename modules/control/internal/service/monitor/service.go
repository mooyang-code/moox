package monitor

import (
	"context"

	"github.com/mooyang-code/moox/modules/control/internal/service/monitor/model"
)

// Service 监控服务接口
type Service interface {
	// ========== 主机监控配置 ==========

	// EnableMonitor 启用主机监控
	EnableMonitor(ctx context.Context, hostID int) error

	// DisableMonitor 禁用主机监控
	DisableMonitor(ctx context.Context, hostID int) error

	// IsMonitorEnabled 检查主机是否启用监控
	IsMonitorEnabled(ctx context.Context, hostID int) (bool, error)

	// ========== 监控数据查询 ==========

	// GetCurrentMetrics 获取指定主机的当前监控指标
	// hostIDs 为空时返回所有启用监控的主机
	GetCurrentMetrics(ctx context.Context, hostIDs []int) ([]*model.HostMetrics, error)

	// GetHistoryMetrics 获取主机历史监控数据
	// duration: 时间范围（如 "1h", "24h", "7d"）
	GetHistoryMetrics(ctx context.Context, hostAddress string, duration string) ([]*model.HistoryPoint, error)

	// TestNodeExporter 测试 Node Exporter 连通性
	TestNodeExporter(ctx context.Context, hostID int) (*TestResult, error)

	// ========== 内部方法（供定时器调用） ==========

	// CollectAll 执行一次完整采集（由定时器调用）
	CollectAll(ctx context.Context) error

	// CleanHistory 清理历史监控数据（由定时器调用）
	CleanHistory(ctx context.Context, keepDays int) error
}

// TestResult 连通性测试结果
type TestResult struct {
	Reachable    bool   `json:"reachable"`
	Message      string `json:"message"`
	DurationMs   int64  `json:"duration_ms"`
	MetricsCount int    `json:"metrics_count"`
}
