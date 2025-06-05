package nats

// MessageConsumer 定义消息消费者接口
type MessageConsumer interface {
	Connect(serverURL string) error // 连接到NATS服务器
	Close() error                   // 关闭连接
	Subscribe(subject string) error // 订阅主题
	Consume() error                 // 开始消费消息
	SetConsumerName(name string)    // 设置消费者名称
}

// MessageHandler 定义消息处理函数接口
type MessageHandler func(data []byte) error
