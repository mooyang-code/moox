package types

import "context"

// Publisher 消息发布器接口
type Publisher interface {
	// Connect 连接到消息服务器
	Connect(ctx context.Context) error

	// Close 关闭连接
	Close() error

	// Publish 发布消息（返回序列号）
	Publish(ctx context.Context, subject string, data []byte) (string, error)

	// PublishMsg 发布完整Message对象
	PublishMsg(ctx context.Context, msg *Message) error

	// IsConnected 检查连接状态
	IsConnected() bool

	// GetOptions 获取配置
	GetOptions() PublisherOptions
}
