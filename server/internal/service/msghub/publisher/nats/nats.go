package nats

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/msghub"
	"github.com/mooyang-code/moox/server/internal/service/msghub/publisher/registry"
	"github.com/mooyang-code/moox/server/internal/service/msghub/types"
	"github.com/nats-io/nats.go"
)

func init() {
	// 注册NATS发布器类型
	registry.RegisterPublisherType(msghub.NATSPublisherType, NewNATSPublisher)
}

// NATSPublisher NATS消息发布器
type NATSPublisher struct {
	options        types.PublisherOptions
	nc             *nats.Conn
	js             nats.JetStreamContext
	connected      bool
	prePublishHook types.HookFunc
}

// NewNATSPublisher 创建NATS发布器
func NewNATSPublisher(opts types.PublisherOptions) (types.Publisher, error) {
	return &NATSPublisher{
		options:        opts,
		prePublishHook: opts.PrePublishHook,
	}, nil
}

// Connect 连接到NATS服务器
func (p *NATSPublisher) Connect(ctx context.Context) error {
	if p.connected {
		return fmt.Errorf("已连接到NATS服务器")
	}

	// 设置连接超时
	timeout := p.options.ConnectTimeout
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
				fmt.Printf("NATS连接断开: %v\n", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			fmt.Printf("NATS重新连接到: %s\n", nc.ConnectedUrl())
		}),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			if err != nil {
				fmt.Printf("NATS错误: %v\n", err)
			}
		}),
	}

	// 连接到NATS服务器
	nc, err := nats.Connect(p.options.ServerURL, options...)
	if err != nil {
		return fmt.Errorf("连接到NATS服务器失败: %w", err)
	}

	p.nc = nc

	// 创建JetStream上下文
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return fmt.Errorf("创建JetStream上下文失败: %w", err)
	}

	p.js = js

	// 如果提供了StreamName，则创建或更新流
	if p.options.StreamName != "" && len(p.options.StreamSubjects) > 0 {
		_, err = js.AddStream(&nats.StreamConfig{
			Name:     p.options.StreamName,
			Subjects: p.options.StreamSubjects,
			Storage:  nats.FileStorage, // 使用文件存储实现持久化
		})
		if err != nil {
			nc.Close()
			return fmt.Errorf("创建或更新流失败: %w", err)
		}
		fmt.Printf("NATS流已创建/更新: %s\n", p.options.StreamName)
	}

	p.connected = true
	fmt.Printf("已连接到NATS服务器: %s\n", p.options.ServerURL)
	return nil
}

// Close 关闭连接
func (p *NATSPublisher) Close() error {
	if !p.connected {
		return nil
	}

	if p.nc != nil {
		p.nc.Close()
		p.nc = nil
		p.js = nil
	}

	p.connected = false
	fmt.Println("NATS发布器已关闭")
	return nil
}

// Publish 发布消息
func (p *NATSPublisher) Publish(ctx context.Context, subject string, data []byte) (string, error) {
	if !p.connected || p.js == nil {
		return "", fmt.Errorf("未连接到NATS服务器")
	}

	// 发布消息到JetStream
	ack, err := p.js.Publish(subject, data)
	if err != nil {
		return "", fmt.Errorf("发布消息失败: %w", err)
	}

	fmt.Printf("消息已发布: 主题=%s, 序列号=%d\n", subject, ack.Sequence)
	return strconv.FormatUint(ack.Sequence, 10), nil
}

// PublishMsg 发布完整Message对象
func (p *NATSPublisher) PublishMsg(ctx context.Context, msg *types.Message) error {
	if !p.connected || p.js == nil {
		return fmt.Errorf("未连接到NATS服务器")
	}

	// 执行发送前钩子
	if p.prePublishHook != nil {
		if err := p.prePublishHook(msg); err != nil {
			return fmt.Errorf("发送前钩子执行失败: %w", err)
		}
	}

	// 构建NATS消息
	natsMsg := &nats.Msg{
		Subject: msg.Subject,
		Data:    msg.Data,
		Header:  nats.Header{},
	}

	// 添加消息头
	if msg.Headers != nil {
		for k, v := range msg.Headers {
			natsMsg.Header.Set(k, v)
		}
	}

	// 如果有消息ID，添加到Header
	if msg.ID != "" {
		natsMsg.Header.Set("Msg-ID", msg.ID)
	}

	// 发布消息
	ack, err := p.js.PublishMsg(natsMsg)
	if err != nil {
		return fmt.Errorf("发布消息失败: %w", err)
	}

	// 更新消息序列号
	msg.Sequence = ack.Sequence

	fmt.Printf("消息已发布: ID=%s, 主题=%s, 序列号=%d\n", msg.ID, msg.Subject, ack.Sequence)
	return nil
}

// IsConnected 检查连接状态
func (p *NATSPublisher) IsConnected() bool {
	return p.connected && p.nc != nil && p.nc.IsConnected()
}

// GetOptions 获取配置
func (p *NATSPublisher) GetOptions() types.PublisherOptions {
	return p.options
}
