// Package nats 提供基于 NATS 的底层消息传输实现，支持 JetStream 消息发布和持久化。
package nats

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/nats-io/nats.go"
)

// NATSProducer 实现基于 NATS/JetStream 的消息生产者。
type NATSProducer struct {
	options   transport.ProducerOptions
	nc        *nats.Conn
	js        nats.JetStreamContext
	connected bool
}

// 包初始化时自动注册 NATS 生产者类型。
func init() {
	transport.RegisterProducerKind(transport.ProducerKindNATS, NewProducer)
	log.Infof("NATS消息生产者类型已注册")
}

// NewProducer 创建新的 NATS 生产者实例。
func NewProducer(opts transport.ProducerOptions) (transport.Producer, error) {
	// 验证必要的配置
	if opts.ServerURL == "" {
		opts.ServerURL = nats.DefaultURL // 使用默认URL（本机4222）
	}

	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = 10 * time.Second // 默认连接超时
	}

	return &NATSProducer{
		options:   opts,
		connected: false,
	}, nil
}

// Connect 连接到NATS服务器并初始化JetStream
func (p *NATSProducer) Connect(ctx context.Context) error {
	_ = ctx
	if p.connected {
		return nil // 已经连接，直接返回
	}

	// 设置连接选项
	options := []nats.Option{
		nats.Timeout(p.options.ConnectTimeout),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(5),
		nats.ReconnectWait(1 * time.Second),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			log.Errorf("断开连接: %v", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Infof("重新连接到 %s", nc.ConnectedUrl())
		}),
		nats.ErrorHandler(func(nc *nats.Conn, sub *nats.Subscription, err error) {
			log.Errorf("错误: %v", err)
		}),
	}

	// 连接到NATS服务器
	nc, err := nats.Connect(p.options.ServerURL, options...)
	if err != nil {
		return fmt.Errorf("无法连接到NATS服务器: %w", err)
	}
	p.nc = nc

	// 获取JetStream上下文
	js, err := nc.JetStream()
	if err != nil {
		p.nc.Close()
		return fmt.Errorf("无法获取JetStream上下文: %w", err)
	}
	p.js = js

	// 如果提供了StreamName，则创建或更新流
	if p.options.StreamName != "" && len(p.options.StreamSubjects) > 0 {
		err = ensureStream(js, &nats.StreamConfig{
			Name:     p.options.StreamName,
			Subjects: p.options.StreamSubjects,
			Storage:  nats.FileStorage, // 使用文件存储以实现持久化
		})
		if err != nil {
			p.nc.Close()
			return fmt.Errorf("无法创建流: %w", err)
		}
		log.Infof("已创建或更新流 %s，主题: %v", p.options.StreamName, p.options.StreamSubjects)
	}

	p.connected = true
	log.Infof("已连接到NATS服务器: %s", p.options.ServerURL)
	return nil
}

type streamManager interface {
	AddStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error)
	UpdateStream(cfg *nats.StreamConfig, opts ...nats.JSOpt) (*nats.StreamInfo, error)
}

func ensureStream(manager streamManager, cfg *nats.StreamConfig) error {
	if _, err := manager.AddStream(cfg); err != nil {
		if errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			_, updateErr := manager.UpdateStream(cfg)
			return updateErr
		}
		return err
	}
	return nil
}

// Close 关闭与NATS服务器的连接
func (p *NATSProducer) Close() error {
	if !p.connected || p.nc == nil {
		return nil
	}

	p.nc.Close()
	p.connected = false
	log.Infof("已关闭NATS连接")
	return nil
}

// Send 发送消息到指定主题。
func (p *NATSProducer) Send(ctx context.Context, msg *transport.Message) error {
	_ = ctx
	if !p.connected || p.js == nil {
		return fmt.Errorf("未连接到NATS服务器")
	}
	if msg == nil || msg.Subject == "" {
		return fmt.Errorf("消息主题不能为空")
	}
	if msg.ID == "" {
		msg.ID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	if msg.Time.IsZero() {
		msg.Time = time.Now()
	}

	ack, err := p.js.Publish(msg.Subject, msg.Data)
	if err != nil {
		return fmt.Errorf("发布消息失败: %w", err)
	}
	log.Debugf("消息已发布: 主题=%s, 序列号=%d", msg.Subject, ack.Sequence)
	return nil
}

// Subscribe 订阅指定主题并把消息交给 handler 处理。
func (p *NATSProducer) Subscribe(ctx context.Context, subject string, handler transport.MessageHandler) (transport.Subscription, error) {
	_ = ctx
	if !p.connected || p.js == nil {
		return nil, fmt.Errorf("未连接到NATS服务器")
	}
	if subject == "" {
		return nil, fmt.Errorf("消息主题不能为空")
	}
	if handler == nil {
		return nil, fmt.Errorf("消息处理器不能为空")
	}
	consumerName := p.options.ConsumerName
	if consumerName == "" {
		consumerName = "storage_rows_changed_deriver"
	}
	subscription, err := p.js.Subscribe(subject, func(msg *nats.Msg) {
		event := &transport.Message{
			Subject: msg.Subject,
			Data:    msg.Data,
			Time:    time.Now(),
		}
		if err := handler(context.Background(), event); err != nil {
			_ = msg.Nak()
			log.Errorf("处理NATS消息失败: %v", err)
			return
		}
		if err := msg.Ack(); err != nil {
			log.Errorf("确认NATS消息失败: %v", err)
		}
	}, nats.ManualAck(), nats.Durable(consumerName))
	if err != nil {
		return nil, fmt.Errorf("订阅消息失败: %w", err)
	}
	return natsSubscription{subscription: subscription}, nil
}

type natsSubscription struct {
	subscription *nats.Subscription
}

func (s natsSubscription) Close() error {
	if s.subscription == nil {
		return nil
	}
	return s.subscription.Unsubscribe()
}

// IsConnected 检查是否已连接到NATS服务器
func (p *NATSProducer) IsConnected() bool {
	return p.connected && p.nc != nil && p.nc.IsConnected()
}

// Options 获取生产者配置选项。
func (p *NATSProducer) Options() transport.ProducerOptions {
	return p.options
}
