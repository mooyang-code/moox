package msghub

import (
	"context"
	"fmt"
	"trpc.group/trpc-go/trpc-go/log"
	"sync"

	"github.com/mooyang-code/moox/modules/admin/internal/service/msghub/consumer"
	"github.com/mooyang-code/moox/modules/admin/internal/service/msghub/publisher"
	"github.com/mooyang-code/moox/modules/admin/internal/service/msghub/server"
	"github.com/mooyang-code/moox/modules/admin/internal/service/msghub/types"
)

// serviceImpl MsgHub服务实现
type serviceImpl struct {
	options      ServiceOptions
	server       types.MessageServer
	publisherReg *publisherRegistry
	consumerReg  *consumerRegistry
	running      bool
	mu           sync.RWMutex
}

// NewService 创建MsgHub服务
func NewService(opts ServiceOptions) (Service, error) {
	// 创建消息服务器
	msgServer, err := server.NewMessageServer(opts.ServerType, opts.ServerOpts)
	if err != nil {
		return nil, fmt.Errorf("创建消息服务器失败: %w", err)
	}

	// 如果设置了自动启动，启动服务器
	if opts.AutoStart {
		if err := msgServer.Start(); err != nil {
			return nil, fmt.Errorf("启动消息服务器失败: %w", err)
		}
	}

	svc := &serviceImpl{
		options:      opts,
		server:       msgServer,
		publisherReg: newPublisherRegistry(),
		consumerReg:  newConsumerRegistry(),
		running:      opts.AutoStart,
	}

	return svc, nil
}

// RegisterPublisher 注册Publisher
func (s *serviceImpl) RegisterPublisher(name string, publisherType PublisherType, opts types.PublisherOptions) error {
	// 创建Publisher
	pub, err := publisher.NewPublisher(publisherType, opts)
	if err != nil {
		return fmt.Errorf("创建Publisher失败: %w", err)
	}

	// 连接到服务器
	if err := pub.Connect(context.Background()); err != nil {
		return fmt.Errorf("连接到服务器失败: %w", err)
	}

	// 注册Publisher
	if err := s.publisherReg.Register(name, pub); err != nil {
		_ = pub.Close()
		return err
	}

	log.Infof("Publisher已注册: %s (类型: %s)\n", name, publisherType)
	return nil
}

// GetPublisher 获取Publisher
func (s *serviceImpl) GetPublisher(name string) (types.Publisher, error) {
	return s.publisherReg.Get(name)
}

// UnregisterPublisher 注销Publisher
func (s *serviceImpl) UnregisterPublisher(name string) error {
	if err := s.publisherReg.Unregister(name); err != nil {
		return err
	}
	log.Infof("Publisher已注销: %s\n", name)
	return nil
}

// ListPublishers 列出所有Publisher
func (s *serviceImpl) ListPublishers() []string {
	return s.publisherReg.List()
}

// RegisterConsumer 注册Consumer
func (s *serviceImpl) RegisterConsumer(name string, consumerType ConsumerType, opts types.ConsumerOptions) error {
	// 创建Consumer
	c, err := consumer.NewConsumer(consumerType, opts)
	if err != nil {
		return fmt.Errorf("创建Consumer失败: %w", err)
	}

	// 订阅消息
	if err := c.Subscribe(context.Background()); err != nil {
		return fmt.Errorf("订阅消息失败: %w", err)
	}

	// 注册Consumer
	if err := s.consumerReg.Register(name, c); err != nil {
		_ = c.Stop()
		return err
	}

	log.Infof("Consumer已注册: %s (类型: %s)\n", name, consumerType)
	return nil
}

// GetConsumer 获取Consumer
func (s *serviceImpl) GetConsumer(name string) (types.Consumer, error) {
	return s.consumerReg.Get(name)
}

// StartConsumer 启动Consumer
func (s *serviceImpl) StartConsumer(name string) error {
	c, err := s.consumerReg.Get(name)
	if err != nil {
		return err
	}

	if err := c.Start(context.Background()); err != nil {
		return fmt.Errorf("启动Consumer失败: %w", err)
	}

	log.Infof("Consumer已启动: %s\n", name)
	return nil
}

// StopConsumer 停止Consumer
func (s *serviceImpl) StopConsumer(name string) error {
	c, err := s.consumerReg.Get(name)
	if err != nil {
		return err
	}

	if err := c.Stop(); err != nil {
		return fmt.Errorf("停止Consumer失败: %w", err)
	}

	log.Infof("Consumer已停止: %s\n", name)
	return nil
}

// UnregisterConsumer 注销Consumer
func (s *serviceImpl) UnregisterConsumer(name string) error {
	if err := s.consumerReg.Unregister(name); err != nil {
		return err
	}
	log.Infof("Consumer已注销: %s\n", name)
	return nil
}

// ListConsumers 列出所有Consumer
func (s *serviceImpl) ListConsumers() []string {
	return s.consumerReg.List()
}

// Start 启动服务
func (s *serviceImpl) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("服务已在运行")
	}

	// 如果服务器未运行，启动服务器
	if !s.server.IsRunning() {
		if err := s.server.Start(); err != nil {
			return fmt.Errorf("启动消息服务器失败: %w", err)
		}
	}

	s.running = true
	log.Info("MsgHub服务已启动")
	return nil
}

// Stop 停止服务
func (s *serviceImpl) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	// 停止所有Consumer
	if err := s.consumerReg.StopAll(); err != nil {
		log.Errorf("停止Consumer时发生错误: %v\n", err)
	}

	// 关闭所有Publisher
	if err := s.publisherReg.CloseAll(); err != nil {
		log.Errorf("关闭Publisher时发生错误: %v\n", err)
	}

	// 停止服务器
	if err := s.server.Stop(); err != nil {
		log.Errorf("停止消息服务器时发生错误: %v\n", err)
	}

	s.running = false
	log.Info("MsgHub服务已停止")
	return nil
}

// IsRunning 检查服务运行状态
func (s *serviceImpl) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}
