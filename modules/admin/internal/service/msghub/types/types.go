package types

import "time"

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

// Message 消息结构
type Message struct {
	ID       string            // 消息唯一标识
	Subject  string            // 消息主题
	Data     []byte            // 消息数据
	Headers  map[string]string // 消息头
	Time     time.Time         // 消息创建时间
	Sequence uint64            // 消息序列号
}

// AddHeader 添加消息头
func (m *Message) AddHeader(key, value string) {
	if m.Headers == nil {
		m.Headers = make(map[string]string)
	}
	m.Headers[key] = value
}

// GetHeader 获取消息头
func (m *Message) GetHeader(key string) string {
	if m.Headers == nil {
		return ""
	}
	return m.Headers[key]
}

// PublisherOptions Publisher配置选项
type PublisherOptions struct {
	ServerURL      string        // NATS服务器URL
	ConnectTimeout time.Duration // 连接超时时间
	StreamName     string        // 消息流名称
	StreamSubjects []string      // 订阅主题列表
	PrePublishHook HookFunc      // 发送前钩子函数
}

// ConsumerOptions Consumer配置选项
type ConsumerOptions struct {
	ServerURL      string         // NATS服务器URL
	ConnectTimeout time.Duration  // 连接超时时间
	StreamName     string         // 消息流名称
	Subject        string         // 订阅主题
	ConsumerName   string         // 消费者名称
	Handler        MessageHandler // 消息处理器
	PrePushHook    HookFunc       // 推送前钩子函数
	MaxInFlight    int            // 最大并发处理数
	AckWait        time.Duration  // 消息确认等待时间
}

// ServerOptions 消息服务器配置选项
type ServerOptions struct {
	Host     string        // 主机地址
	Port     int           // 端口号
	StoreDir string        // 数据存储目录
	Timeout  time.Duration // 超时时间
}

// MessageHandler 消息处理器函数
type MessageHandler func(msg *Message) error

// HookFunc 钩子函数
type HookFunc func(msg *Message) error
