package types

import "context"

// Consumer 消息消费者接口
type Consumer interface {
	// Subscribe 订阅消息
	Subscribe(ctx context.Context) error

	// Start 启动消费者
	Start(ctx context.Context) error

	// Stop 停止消费者
	Stop() error

	// IsRunning 检查运行状态
	IsRunning() bool

	// GetOptions 获取配置
	GetOptions() ConsumerOptions
}
