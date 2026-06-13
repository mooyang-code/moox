package types

import (
	"context"
)

// Publisher 定义消息发布器接口
type Publisher interface {
	// Connect 连接到消息服务器
	Connect(ctx context.Context) error

	// Close 关闭与消息服务器的连接
	Close() error

	// Publish 发布消息
	Publish(ctx context.Context, subject string, data []byte) (string, error)

	// PublishMsg 发布完整消息对象
	PublishMsg(ctx context.Context, msg *Message) error

	// IsConnected 检查是否已连接到服务器
	IsConnected() bool

	// GetOptions 获取发布器配置选项
	GetOptions() PublisherOptions
}
