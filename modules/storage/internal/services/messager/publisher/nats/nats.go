// Package nats 提供基于NATS的消息发布器实现，支持JetStream消息发布和持久化
package nats

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/publisher/registry"
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/types"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/nats-io/nats.go"
)

// NATSPublisher 实现Publisher接口的NATS发布器
type NATSPublisher struct {
	options   types.PublisherOptions
	nc        *nats.Conn
	js        nats.JetStreamContext
	connected bool
}

// 包初始化时自动注册NATS发布器类型
func init() {
	registry.RegisterPublisherType(constants.NATSPublisherType, NewNATSPublisher)
	log.Infof("NATS消息发布器类型已注册")
}

// NewNATSPublisher 创建新的NATS发布器实例
func NewNATSPublisher(opts types.PublisherOptions) (types.Publisher, error) {
	// 验证必要的配置
	if opts.ServerURL == "" {
		opts.ServerURL = nats.DefaultURL // 使用默认URL（本机4222）
	}

	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = 10 * time.Second // 默认连接超时
	}

	return &NATSPublisher{
		options:   opts,
		connected: false,
	}, nil
}

// Connect 连接到NATS服务器并初始化JetStream
func (p *NATSPublisher) Connect(ctx context.Context) error {
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
		_, err = js.AddStream(&nats.StreamConfig{
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

// Close 关闭与NATS服务器的连接
func (p *NATSPublisher) Close() error {
	if !p.connected || p.nc == nil {
		return nil
	}

	p.nc.Close()
	p.connected = false
	log.Infof("已关闭NATS连接")
	return nil
}

// Publish 发布消息到指定主题
func (p *NATSPublisher) Publish(ctx context.Context, subject string, data []byte) (string, error) {
	if !p.connected || p.js == nil {
		return "", fmt.Errorf("未连接到NATS服务器")
	}

	// 发布消息到JetStream
	ack, err := p.js.Publish(subject, data)
	if err != nil {
		return "", fmt.Errorf("发布消息失败: %w", err)
	}

	log.Debugf("消息已发布: 主题=%s, 序列号=%d", subject, ack.Sequence)
	return strconv.FormatUint(ack.Sequence, 10), nil
}

// PublishMsg 发布完整消息对象
func (p *NATSPublisher) PublishMsg(ctx context.Context, msg *types.Message) error {
	if !p.connected || p.js == nil {
		return fmt.Errorf("未连接到NATS服务器")
	}

	// 如果消息ID为空，则使用当前时间戳生成一个
	if msg.ID == "" {
		msg.ID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}

	// 如果消息时间未设置，则使用当前时间
	if msg.Time.IsZero() {
		msg.Time = time.Now()
	}

	// 发布消息
	_, err := p.Publish(ctx, msg.Subject, msg.Data)
	return err
}

// IsConnected 检查是否已连接到NATS服务器
func (p *NATSPublisher) IsConnected() bool {
	return p.connected && p.nc != nil && p.nc.IsConnected()
}

// GetOptions 获取发布器配置选项
func (p *NATSPublisher) GetOptions() types.PublisherOptions {
	return p.options
}
