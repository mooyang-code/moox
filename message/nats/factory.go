package nats

import (
	"fmt"
	"strings"
)

// CreateMessageConsumer 创建消息消费者(当前系统只有nats类型的消息组件)
func CreateMessageConsumer(connectionString string) (MessageConsumer, error) {
	// 解析连接字符串，格式为 "类型:连接信息"
	parts := strings.SplitN(connectionString, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("连接字符串格式不正确，应为 '类型:连接信息'")
	}

	consumerType := strings.ToLower(parts[0])
	serverURL := parts[1]

	// 使用双斜杠补全URL前缀
	if !strings.HasPrefix(serverURL, "//") {
		serverURL = "//" + serverURL
	}

	// 补全协议前缀
	if !strings.HasPrefix(serverURL, "nats:") {
		serverURL = "nats:" + serverURL
	}

	// 根据类型创建对应的消费者
	switch consumerType {
	case "nats":
		consumer := NewNatsConsumer()
		err := consumer.Connect(serverURL)
		if err != nil {
			return nil, fmt.Errorf("连接NATS服务器失败: %v", err)
		}
		return consumer, nil
	// 可以在此添加其他消息队列类型的支持
	default:
		return nil, fmt.Errorf("不支持的消息队列类型: %s", consumerType)
	}
}
