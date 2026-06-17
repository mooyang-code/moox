package nats

import (
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

// NatsConsumer 实现 MessageConsumer 接口
type NatsConsumer struct {
	conn         *nats.Conn
	js           nats.JetStreamContext
	streamName   string
	consumerName string
	subject      string
	maxWaitTime  time.Duration
	handler      MessageHandler
}

// NewNatsConsumer 创建新的NATS消费者
func NewNatsConsumer() MessageConsumer {
	return &NatsConsumer{
		streamName:   "storage_events",
		consumerName: "MY_CONSUMER",
		maxWaitTime:  5 * time.Second,
		handler:      defaultMessageHandler,
	}
}

// 默认消息处理函数
func defaultMessageHandler(data []byte) error {
	fmt.Printf("收到消息: %s\n", string(data))
	return nil
}

// Connect 连接到NATS服务器
func (nc *NatsConsumer) Connect(serverURL string) error {
	// 连接到NATS服务器
	conn, err := nats.Connect(serverURL)
	if err != nil {
		return fmt.Errorf("连接NATS服务器失败: %v", err)
	}
	nc.conn = conn

	// 获取JetStream上下文
	js, err := conn.JetStream()
	if err != nil {
		nc.Close()
		return fmt.Errorf("获取JetStream上下文失败: %v", err)
	}
	nc.js = js

	fmt.Println("已成功连接到NATS服务器")
	return nil
}

// Close 关闭NATS连接
func (nc *NatsConsumer) Close() error {
	if nc.conn != nil {
		nc.conn.Close()
		fmt.Println("已关闭NATS连接")
	}
	return nil
}

// SetConsumerName 设置消费者名称
func (nc *NatsConsumer) SetConsumerName(name string) {
	nc.consumerName = name
}

// SetMaxWaitTime 设置最大等待时间
func (nc *NatsConsumer) SetMaxWaitTime(milliseconds int) {
	nc.maxWaitTime = time.Duration(milliseconds) * time.Millisecond
}

// SetMessageHandler 设置消息处理函数
func (nc *NatsConsumer) SetMessageHandler(handler MessageHandler) {
	nc.handler = handler
}

// Subscribe 订阅主题
func (nc *NatsConsumer) Subscribe(subject string) error {
	if nc.js == nil {
		return fmt.Errorf("未连接到NATS服务器")
	}

	nc.subject = subject

	// 创建消费者
	_, err := nc.js.AddConsumer(nc.streamName, &nats.ConsumerConfig{
		Durable:       nc.consumerName,
		AckPolicy:     nats.AckExplicitPolicy, // 需要显式确认
		DeliverPolicy: nats.DeliverAllPolicy,  // 从头开始接收所有消息
	})
	if err != nil {
		return fmt.Errorf("创建消费者失败: %v", err)
	}

	fmt.Printf("已订阅主题: %s\n", subject)
	return nil
}

// Consume 开始消费消息
func (nc *NatsConsumer) Consume() error {
	if nc.js == nil {
		return fmt.Errorf("未连接到NATS服务器")
	}

	if nc.subject == "" {
		return fmt.Errorf("未指定订阅主题")
	}

	// 订阅消息
	sub, err := nc.js.PullSubscribe(nc.subject, nc.consumerName, nats.BindStream(nc.streamName))
	if err != nil {
		return fmt.Errorf("订阅失败: %v", err)
	}

	fmt.Println("开始消费消息...")

	// 持续消费消息
	for {
		// 拉取消息
		msgs, err := sub.Fetch(1, nats.MaxWait(nc.maxWaitTime))
		if err != nil {
			if err == nats.ErrTimeout {
				// 超时，继续尝试
				fmt.Println("等待新消息中...")
				continue
			}
			return fmt.Errorf("拉取消息失败: %v", err)
		}

		// 处理消息
		for _, m := range msgs {
			// 调用消息处理函数
			err = nc.handler(m.Data)
			if err != nil {
				log.Printf("处理消息失败: %v", err)
			} else {
				// 确认消息
				err = m.Ack()
				if err != nil {
					log.Printf("确认消息失败: %v", err)
				}
			}
		}
	}
}
