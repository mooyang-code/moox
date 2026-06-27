package registry

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/admin/internal/service/msghub/types"
	"sync"
)

// CreateMessageServer 创建消息服务器的函数类型
type CreateMessageServer func(opts types.ServerOptions) (types.MessageServer, error)

// 服务器注册表
var serverRegistry struct {
	once     sync.Once
	registry map[types.ServerType]CreateMessageServer
}

// 初始化服务器注册表
func initServerRegistry() {
	serverRegistry.once.Do(func() {
		serverRegistry.registry = make(map[types.ServerType]CreateMessageServer)
	})
}

// RegisterServerType 注册服务器类型
func RegisterServerType(serverType types.ServerType, constructor CreateMessageServer) {
	initServerRegistry()
	serverRegistry.registry[serverType] = constructor
	fmt.Printf("服务器类型已注册: %s\n", serverType)
}

// GetServerConstructor 获取服务器构造函数
func GetServerConstructor(serverType types.ServerType) (CreateMessageServer, error) {
	initServerRegistry()
	constructor, exists := serverRegistry.registry[serverType]
	if !exists {
		return nil, fmt.Errorf("未知的服务器类型: %s", serverType)
	}
	return constructor, nil
}

// ListServerTypes 列出所有已注册的服务器类型
func ListServerTypes() []types.ServerType {
	initServerRegistry()
	var types []types.ServerType
	for t := range serverRegistry.registry {
		types = append(types, t)
	}
	return types
}
