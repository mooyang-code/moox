package nats

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/msghub"
	"github.com/mooyang-code/moox/server/internal/service/msghub/consumer/registry"
	"github.com/mooyang-code/moox/server/internal/service/msghub/types"
	"github.com/nats-io/nats.go"
)

func init() {
	// 注册NATS消费者类型
	registry.RegisterConsumerType(msghub.NATSConsumerType, NewNATSConsumer)
}

// NATSConsumer NATS消息消费者
type NATSConsumer struct {
	options      types.ConsumerOptions
	nc           *nats.Conn
	js           nats.JetStreamContext
	subscription *nats.Subscription
	handler      types.MessageHandler
	prePushHook  types.HookFunc
	running      bool
	mu           sync.RWMutex
	stopChan     chan struct{}
}

// NewNATSConsumer 创建NATS消费者
func NewNATSConsumer(opts types.ConsumerOptions) (types.Consumer, error) {
	if opts.Handler == nil {
		return nil, fmt.Errorf("消息处理器不能为空")
	}

	// 设置默认值
	if opts.MaxInFlight == 0 {
		opts.MaxInFlight = 100
	}
	if opts.AckWait == 0 {
		opts.AckWait = 30 * time.Second
	}

	return &NATSConsumer{
		options:     opts,
		handler:     opts.Handler,
		prePushHook: opts.PrePushHook,
		stopChan:    make(chan struct{}),
	}, nil
}

// Subscribe 订阅消息
func (c *NATSConsumer) Subscribe(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.subscription != nil {
		return fmt.Errorf("已经订阅")
	}

	// 设置连接超时
	timeout := c.options.ConnectTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	// 连接选项
	options := []nats.Option{
		nats.Timeout(timeout),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(5),
		nats.ReconnectWait(1 * time.Second),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			if err != nil {
				fmt.Printf("NATS消费者连接断开: %v\n", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			fmt.Printf("NATS消费者重新连接到: %s\n", nc.ConnectedUrl())
		}),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			if err != nil {
				fmt.Printf("NATS消费者错误: %v\n", err)
			}
		}),
	}

	// 连接到NATS服务器
	nc, err := nats.Connect(c.options.ServerURL, options...)
	if err != nil {
		return fmt.Errorf("连接到NATS服务器失败: %w", err)
	}

	c.nc = nc

	// 创建JetStream上下文
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return fmt.Errorf("创建JetStream上下文失败: %w", err)
	}

	c.js = js

	// 订阅消息
	sub, err := js.Subscribe(
		c.options.Subject,
		c.messageCallback,
		nats.Durable(c.options.ConsumerName),
		nats.ManualAck(),
		nats.AckWait(c.options.AckWait),
		nats.MaxAckPending(c.options.MaxInFlight),
	)
	if err != nil {
		nc.Close()
		return fmt.Errorf("订阅消息失败: %w", err)
	}

	c.subscription = sub

	fmt.Printf("NATS消费者已订阅: 主题=%s, 消费者名=%s\n", c.options.Subject, c.options.ConsumerName)
	return nil
}

// messageCallback 消息回调函数
func (c *NATSConsumer) messageCallback(natsMsg *nats.Msg) {
	// 检查是否正在运行
	c.mu.RLock()
	running := c.running
	c.mu.RUnlock()

	if !running {
		// 如果已停止，拒绝消息
		_ = natsMsg.Nak()
		return
	}

	// 转换为内部Message
	message := &types.Message{
		Subject:  natsMsg.Subject,
		Data:     natsMsg.Data,
		Time:     time.Now(),
		Headers:  make(map[string]string),
	}

	// 提取消息头
	if natsMsg.Header != nil {
		for k := range natsMsg.Header {
			message.Headers[k] = natsMsg.Header.Get(k)
		}
		// 提取消息ID
		if msgID := natsMsg.Header.Get("Msg-ID"); msgID != "" {
			message.ID = msgID
		}
	}

	// 提取元数据
	meta, err := natsMsg.Metadata()
	if err == nil {
		message.Sequence = meta.Sequence.Stream
	}

	// 执行推送前钩子
	if c.prePushHook != nil {
		if err := c.prePushHook(message); err != nil {
			fmt.Printf("推送前钩子执行失败: %v, 消息ID=%s\n", err, message.ID)
			_ = natsMsg.Nak()
			return
		}
	}

	// 调用业务处理器
	if err := c.handler(message); err != nil {
		fmt.Printf("消息处理失败: %v, 消息ID=%s\n", err, message.ID)
		_ = natsMsg.Nak()
		return
	}

	// 确认消息
	if err := natsMsg.Ack(); err != nil {
		fmt.Printf("消息确认失败: %v, 消息ID=%s\n", err, message.ID)
	} else {
		fmt.Printf("消息处理成功: ID=%s, 主题=%s, 序列号=%d\n", message.ID, message.Subject, message.Sequence)
	}
}

// Start 启动消费者
func (c *NATSConsumer) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return fmt.Errorf("消费者已在运行")
	}

	// 如果还未订阅，先订阅
	if c.subscription == nil {
		c.mu.Unlock()
		if err := c.Subscribe(ctx); err != nil {
			c.mu.Lock()
			return err
		}
		c.mu.Lock()
	}

	c.running = true
	fmt.Printf("NATS消费者已启动: %s\n", c.options.ConsumerName)
	return nil
}

// Stop 停止消费者
func (c *NATSConsumer) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return nil
	}

	c.running = false

	// 关闭订阅
	if c.subscription != nil {
		if err := c.subscription.Drain(); err != nil {
			fmt.Printf("关闭订阅失败: %v\n", err)
		}
		c.subscription = nil
	}

	// 关闭连接
	if c.nc != nil {
		c.nc.Close()
		c.nc = nil
		c.js = nil
	}

	fmt.Printf("NATS消费者已停止: %s\n", c.options.ConsumerName)
	return nil
}

// IsRunning 检查运行状态
func (c *NATSConsumer) IsRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.running
}

// GetOptions 获取配置
func (c *NATSConsumer) GetOptions() types.ConsumerOptions {
	return c.options
}
