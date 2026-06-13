package types

import (
	"time"
)

// ServerOptions 定义消息服务器的配置选项
type ServerOptions struct {
	Host     string        // 服务器主机地址
	Port     int           // 服务器端口
	StoreDir string        // 存储目录路径
	Timeout  time.Duration // 连接超时时间
}

// MessageServer 定义消息服务器接口
type MessageServer interface {
	// Start 启动消息服务器
	Start() error

	// Stop 停止消息服务器
	Stop() error

	// IsRunning 检查服务器是否正在运行
	IsRunning() bool

	// GetOptions 获取服务器配置选项
	GetOptions() ServerOptions
}
