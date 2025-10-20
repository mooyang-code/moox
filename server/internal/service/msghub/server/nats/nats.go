package nats

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/msghub"
	"github.com/mooyang-code/moox/server/internal/service/msghub/server/registry"
	"github.com/mooyang-code/moox/server/internal/service/msghub/types"
	"github.com/nats-io/nats-server/v2/server"
)

func init() {
	// 注册NATS服务器类型
	registry.RegisterServerType(msghub.NATSServerType, NewNATSServer)
}

// NATSServer NATS消息服务器
type NATSServer struct {
	options types.ServerOptions
	server  *server.Server
	running bool
}

// NewNATSServer 创建NATS服务器
func NewNATSServer(opts types.ServerOptions) (types.MessageServer, error) {
	return &NATSServer{
		options: opts,
	}, nil
}

// Start 启动服务器
func (n *NATSServer) Start() error {
	if n.running {
		return fmt.Errorf("NATS服务器已在运行")
	}

	// 设置默认超时
	timeout := n.options.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	// 创建NATS服务器配置
	opts := &server.Options{
		Host:      n.options.Host,
		Port:      n.options.Port,
		JetStream: true,              // 启用JetStream
		StoreDir:  n.options.StoreDir, // 持久化存储目录
	}

	// 初始化NATS服务器
	ns, err := server.NewServer(opts)
	if err != nil {
		return fmt.Errorf("创建NATS服务器失败: %w", err)
	}

	n.server = ns

	// 非阻塞启动服务器
	go n.server.Start()

	// 等待服务器启动
	if !n.server.ReadyForConnections(timeout) {
		return fmt.Errorf("NATS服务器启动超时: %s", timeout)
	}

	n.running = true
	fmt.Printf("NATS服务器已启动: %s:%d (存储目录: %s)\n", n.options.Host, n.options.Port, n.options.StoreDir)
	return nil
}

// Stop 停止服务器
func (n *NATSServer) Stop() error {
	if !n.running {
		return nil
	}

	if n.server != nil {
		n.server.Shutdown()
		n.server = nil
	}

	n.running = false
	fmt.Println("NATS服务器已停止")
	return nil
}

// IsRunning 检查运行状态
func (n *NATSServer) IsRunning() bool {
	return n.running && n.server != nil && n.server.Running()
}

// GetOptions 获取配置
func (n *NATSServer) GetOptions() types.ServerOptions {
	return n.options
}
