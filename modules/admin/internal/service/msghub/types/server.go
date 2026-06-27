package types

// MessageServer 消息服务器接口
type MessageServer interface {
	// Start 启动服务器
	Start() error

	// Stop 停止服务器
	Stop() error

	// IsRunning 检查运行状态
	IsRunning() bool

	// GetOptions 获取配置
	GetOptions() ServerOptions
}
