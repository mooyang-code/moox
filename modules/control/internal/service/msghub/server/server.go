package server

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/control/internal/service/msghub/server/registry"
	"github.com/mooyang-code/moox/modules/control/internal/service/msghub/types"

	// 导入NATS实现以触发init注册
	_ "github.com/mooyang-code/moox/modules/control/internal/service/msghub/server/nats"
)

// NewMessageServer 创建消息服务器
func NewMessageServer(serverType types.ServerType, opts types.ServerOptions) (types.MessageServer, error) {
	constructor, err := registry.GetServerConstructor(serverType)
	if err != nil {
		return nil, err
	}

	server, err := constructor(opts)
	if err != nil {
		return nil, fmt.Errorf("创建消息服务器失败: %w", err)
	}

	return server, nil
}

// ListServerTypes 列出所有可用的服务器类型
func ListServerTypes() []types.ServerType {
	return registry.ListServerTypes()
}
