// Package registry 提供消息服务器的注册表管理，支持动态注册和获取不同类型的消息服务器
package registry

import (
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/types"
)

// CreateMessageServer 创建消息服务器的工厂函数类型
type CreateMessageServer func(opts types.ServerOptions) (types.MessageServer, error)

// 消息服务器注册表
var serverRegistry struct {
	once     sync.Once
	registry map[constants.ServerType]CreateMessageServer
}

// 初始化注册表
func initServerRegistry() {
	serverRegistry.once.Do(func() {
		serverRegistry.registry = make(map[constants.ServerType]CreateMessageServer)
	})
}

// RegisterServerType 注册服务器类型到系统
func RegisterServerType(serverType constants.ServerType, constructor CreateMessageServer) {
	initServerRegistry()
	serverRegistry.registry[serverType] = constructor
}

// GetMessageServer 消息服务器工厂函数
func GetMessageServer(serverType constants.ServerType, opts types.ServerOptions) (types.MessageServer, error) {
	initServerRegistry()

	// 尝试根据服务器类型获取构造函数
	constructor, exists := serverRegistry.registry[serverType]
	if !exists {
		return nil, fmt.Errorf("无效的服务器类型: %s", serverType)
	}

	// 调用构造函数创建服务器实例
	server, err := constructor(opts)
	if err != nil {
		return nil, fmt.Errorf("创建服务器实例失败: %v", err)
	}
	return server, nil
}
