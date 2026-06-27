package msghub

import "github.com/mooyang-code/moox/modules/admin/internal/service/msghub/types"

// PublisherType 发布器类型
type PublisherType = types.PublisherType

const (
	// NATSPublisherType NATS发布器类型
	NATSPublisherType = types.NATSPublisherType
)

// ConsumerType 消费者类型
type ConsumerType = types.ConsumerType

const (
	// NATSConsumerType NATS消费者类型
	NATSConsumerType = types.NATSConsumerType
)

// ServerType 服务器类型
type ServerType = types.ServerType

const (
	// NATSServerType NATS服务器类型
	NATSServerType = types.NATSServerType
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
