// Package server 该包主要是用于抽象嵌入式消息组件的服务端，非嵌入式消息组件不需要
package server

import (
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/access/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	_ "github.com/mooyang-code/moox/modules/storage/internal/services/messager/server/nats" // 导入NATS服务器包以确保其init()函数被执行
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/server/registry"
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/types"
	"trpc.group/trpc-go/trpc-go/log"
)

// 为了向后兼容，定义类型别名
type ServerOptions = types.ServerOptions
type MessageServer = types.MessageServer

// NewMessageServer 消息服务器工厂函数
func NewMessageServer(serverType constants.ServerType, opts types.ServerOptions) (types.MessageServer, error) {
	return registry.GetMessageServer(serverType, opts)
}

// SetupMessageServer 创建并启动消息服务器
// 参数:
//   - msgConf: 消息服务器配置
//
// 返回:
//   - 消息服务器实例
//   - 错误信息
func SetupMessageServer(msgConf config.MessageServerConf) (types.MessageServer, error) {
	// 如果消息服务未启用，则直接返回
	if !msgConf.Enable {
		return nil, nil
	}

	// 创建消息服务器
	log.Infof("正在创建消息服务器...")
	msgServer, err := NewMessageServer(constants.ServerType(msgConf.Name),
		ServerOptions{
			Host:     msgConf.Host,     // 主机地址
			Port:     msgConf.Port,     // 端口
			StoreDir: msgConf.DataPath, // 存储目录
			Timeout:  5 * time.Second,  // 超时时间
		})
	if err != nil {
		log.Errorf("创建消息服务器失败: %v", err)
		return nil, err
	}

	// 启动消息服务器
	log.Infof("正在启动消息服务器...")
	if err := msgServer.Start(); err != nil {
		log.Errorf("启动服务器失败: %v", err)
		return nil, err
	}
	log.Infof("消息服务器已成功启动")

	return msgServer, nil
}
