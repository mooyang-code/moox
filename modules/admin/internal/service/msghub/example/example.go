package main

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/msghub"
	"github.com/mooyang-code/moox/modules/admin/internal/service/msghub/types"
)

func main() {
	// 1. 创建MsgHub服务
	svc, err := msghub.NewService(msghub.ServiceOptions{
		ServerType: msghub.NATSServerType,
		ServerOpts: types.ServerOptions{
			Host:     "127.0.0.1",
			Port:     4222,
			StoreDir: "/tmp/msghub-example",
			Timeout:  5 * time.Second,
		},
		AutoStart: true,
	})
	if err != nil {
		panic(err)
	}
	defer svc.Stop(context.Background())

	// 2. 注册Publisher（带发送前钩子）
	err = svc.RegisterPublisher("order-publisher", msghub.NATSPublisherType, types.PublisherOptions{
		ServerURL:      "nats://127.0.0.1:4222",
		ConnectTimeout: 10 * time.Second,
		StreamName:     "orders",
		StreamSubjects: []string{"order.created", "order.updated"},
		PrePublishHook: func(msg *types.Message) error {
			// 发送前钩子：记录日志、验证、加密等
			fmt.Printf("发送前钩子: 消息ID=%s, 主题=%s\n", msg.ID, msg.Subject)
			// 添加自定义消息头
			msg.AddHeader("X-Source", "order-service")
			msg.AddHeader("X-Timestamp", time.Now().Format(time.RFC3339))
			return nil
		},
	})
	if err != nil {
		panic(err)
	}

	// 3. 注册Consumer（带推送前钩子）
	err = svc.RegisterConsumer("order-consumer", msghub.NATSConsumerType, types.ConsumerOptions{
		ServerURL:      "nats://127.0.0.1:4222",
		ConnectTimeout: 10 * time.Second,
		StreamName:     "orders",
		Subject:        "order.created",
		ConsumerName:   "order-processor",
		MaxInFlight:    100,
		AckWait:        30 * time.Second,
		PrePushHook: func(msg *types.Message) error {
			// 推送前钩子：记录日志、过滤、解密等
			fmt.Printf("推送前钩子: 消息ID=%s, 主题=%s\n", msg.ID, msg.Subject)
			// 验证消息头
			if source := msg.GetHeader("X-Source"); source == "" {
				return fmt.Errorf("缺少消息来源")
			}
			return nil
		},
		Handler: func(msg *types.Message) error {
			// 业务逻辑处理
			fmt.Printf("处理订单消息: ID=%s, 数据=%s\n", msg.ID, string(msg.Data))
			// 这里可以调用业务逻辑处理订单
			return nil
		},
	})
	if err != nil {
		panic(err)
	}

	// 4. 启动Consumer
	if err := svc.StartConsumer("order-consumer"); err != nil {
		panic(err)
	}

	// 5. 发送消息
	pub, err := svc.GetPublisher("order-publisher")
	if err != nil {
		panic(err)
	}

	// 发送多条消息
	for i := 1; i <= 5; i++ {
		msg := msghub.NewMessage("order.created", []byte(fmt.Sprintf(`{"order_id": "%d", "amount": 100}`, i)))
		if err := pub.PublishMsg(context.Background(), msg); err != nil {
			fmt.Printf("发送消息失败: %v\n", err)
		}
		time.Sleep(1 * time.Second)
	}

	// 6. 等待消息处理
	fmt.Println("等待消息处理...")
	time.Sleep(10 * time.Second)

	// 7. 查看状态
	fmt.Printf("Publishers: %v\n", svc.ListPublishers())
	fmt.Printf("Consumers: %v\n", svc.ListConsumers())
}
