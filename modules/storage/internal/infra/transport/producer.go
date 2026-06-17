package transport

import (
	"context"
)

// Producer 定义底层消息生产者接口。
type Producer interface {
	// Connect 连接到消息服务器
	Connect(ctx context.Context) error

	// Close 关闭与消息服务器的连接
	Close() error

	// Send 发送完整消息对象
	Send(ctx context.Context, msg *Message) error

	// IsConnected 检查是否已连接到服务器
	IsConnected() bool

	// Options 获取生产者配置选项
	Options() ProducerOptions
}
