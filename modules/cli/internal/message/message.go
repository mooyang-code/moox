package message

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/cli/internal/config"
	"github.com/mooyang-code/moox/modules/cli/internal/message/nats"
)

// MessageOperator 消息操作结构
type MessageOperator struct {
	config      *config.Config
	consumer    nats.MessageConsumer
	isConnected bool
}

// NewMessageOperator 创建新的消息操作实例
func NewMessageOperator(cfg *config.Config) *MessageOperator {
	return &MessageOperator{
		config:      cfg,
		isConnected: false,
	}
}

// Close 关闭连接
func (op *MessageOperator) Close() {
	if op.isConnected && op.consumer != nil {
		op.consumer.Close()
		op.isConnected = false
	}
}

// initConsumer 初始化消息消费者
func (op *MessageOperator) initConsumer() error {
	if op.isConnected {
		return nil
	}

	// 检查配置
	msgCfg := op.config.Message
	if msgCfg == nil || msgCfg.Server == "" {
		return fmt.Errorf("消息服务器配置缺失")
	}

	// 创建消费者
	consumer, err := nats.CreateMessageConsumer(msgCfg.Server)
	if err != nil {
		return fmt.Errorf("创建消息消费者失败: %v", err)
	}

	// 设置消费者配置
	if msgCfg.Consumer != "" {
		consumer.SetConsumerName(msgCfg.Consumer)
	}
	op.consumer = consumer
	op.isConnected = true
	return nil
}

// ConsumeMessages 消费消息队列中的消息
func (op *MessageOperator) ConsumeMessages() error {
	// 初始化消费者
	err := op.initConsumer()
	if err != nil {
		return err
	}

	// 检查配置
	msgCfg := op.config.Message
	if msgCfg == nil || msgCfg.Subject == "" {
		return fmt.Errorf("消息主题配置缺失")
	}

	// 订阅主题
	err = op.consumer.Subscribe(msgCfg.Subject)
	if err != nil {
		return fmt.Errorf("订阅消息主题失败: %v", err)
	}

	// 开始消费消息
	return op.consumer.Consume()
}
