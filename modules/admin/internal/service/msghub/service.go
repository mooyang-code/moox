package msghub

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/service/msghub/types"
)

// Service MsgHub服务接口
type Service interface {
	// Publisher管理
	RegisterPublisher(name string, publisherType PublisherType, opts types.PublisherOptions) error
	GetPublisher(name string) (types.Publisher, error)
	UnregisterPublisher(name string) error
	ListPublishers() []string

	// Consumer管理
	RegisterConsumer(name string, consumerType ConsumerType, opts types.ConsumerOptions) error
	GetConsumer(name string) (types.Consumer, error)
	StartConsumer(name string) error
	StopConsumer(name string) error
	UnregisterConsumer(name string) error
	ListConsumers() []string

	// 生命周期管理
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	IsRunning() bool
}

// ServiceOptions Service配置选项
type ServiceOptions struct {
	ServerType ServerType          // 服务器类型
	ServerOpts types.ServerOptions // 服务器配置
	AutoStart  bool                // 是否自动启动服务器
}
