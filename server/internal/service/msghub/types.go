package msghub

// PublisherType 发布器类型
type PublisherType string

const (
	// NATSPublisherType NATS发布器类型
	NATSPublisherType PublisherType = "nats"
)

// ConsumerType 消费者类型
type ConsumerType string

const (
	// NATSConsumerType NATS消费者类型
	NATSConsumerType ConsumerType = "nats"
)

// ServerType 服务器类型
type ServerType string

const (
	// NATSServerType NATS服务器类型
	NATSServerType ServerType = "nats"
)

// 消息状态常量
const (
	// MessageStatusPending 待处理
	MessageStatusPending = 0
	// MessageStatusProcessing 处理中
	MessageStatusProcessing = 1
	// MessageStatusSuccess 处理成功
	MessageStatusSuccess = 2
	// MessageStatusFailed 处理失败
	MessageStatusFailed = 3
)
