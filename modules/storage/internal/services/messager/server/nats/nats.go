// Package nats 提供基于NATS的消息服务器实现，支持JetStream持久化消息传递
package nats

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/server/registry"
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/types"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/nats-io/nats-server/v2/server"
)

// NATSServer 实现MessageServer接口
type NATSServer struct {
	options types.ServerOptions
	server  *server.Server
	running bool
}

// 包初始化时自动注册NATS服务器类型
func init() {
	registry.RegisterServerType(constants.NATSServerType, NewNATSServer)
	log.Infof("NATS消息服务器类型已注册")
}

// NewNATSServer 创建新的NATS服务器实例
func NewNATSServer(opts types.ServerOptions) (types.MessageServer, error) {
	return &NATSServer{
		options: opts,
		running: false,
	}, nil
}

// Start 启动NATS服务器
func (n *NATSServer) Start() error {
	if n.running {
		return fmt.Errorf("NATS server is already running")
	}

	// 创建NATS服务器配置
	opts := &server.Options{
		Host:      n.options.Host,
		Port:      n.options.Port,
		JetStream: true, // 直接启用JetStream
		StoreDir:  n.options.StoreDir,
	}
	if opts.Host == "" {
		return fmt.Errorf("NATS server Host is empty")
	}
	if opts.Port == 0 {
		return fmt.Errorf("NATS server Port is empty")
	}
	if opts.StoreDir == "" {
		return fmt.Errorf("NATS server DataPath is empty")
	}

	// 初始化NATS服务器
	ns, err := server.NewServer(opts)
	if err != nil {
		return fmt.Errorf("failed to create NATS server: %w", err)
	}
	n.server = ns

	// 启动NATS服务器（非阻塞）
	go n.server.Start()

	// 等待服务器启动完成
	timeout := n.options.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second // 默认超时时间
	}

	if !n.server.ReadyForConnections(timeout) {
		return fmt.Errorf("NATS server did not start within %s", timeout)
	}

	n.running = true
	log.Infof("NATS server started on %s:%d with JetStream enabled",
		n.options.Host, n.options.Port)
	return nil
}

// Stop 停止NATS服务器
func (n *NATSServer) Stop() error {
	if !n.running || n.server == nil {
		return fmt.Errorf("NATS server is not running")
	}

	n.server.Shutdown()
	n.running = false
	log.Infof("NATS server stopped")
	return nil
}

// IsRunning 检查服务器是否正在运行
func (n *NATSServer) IsRunning() bool {
	return n.running
}

// GetOptions 获取服务器配置选项
func (n *NATSServer) GetOptions() types.ServerOptions {
	return n.options
}
