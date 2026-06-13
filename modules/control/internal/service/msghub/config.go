package msghub

import (
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/service/msghub/types"
)

// Config MsgHub配置
type Config struct {
	Server     ServerConfig      `yaml:"server" json:"server"`
	Publishers []PublisherConfig `yaml:"publishers" json:"publishers"`
	Consumers  []ConsumerConfig  `yaml:"consumers" json:"consumers"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Enable   bool   `yaml:"enable" json:"enable"`       // 是否启用
	Type     string `yaml:"type" json:"type"`           // 服务器类型 (nats)
	Host     string `yaml:"host" json:"host"`           // 主机地址
	Port     int    `yaml:"port" json:"port"`           // 端口号
	StoreDir string `yaml:"store_dir" json:"store_dir"` // 存储目录
}

// PublisherConfig Publisher配置
type PublisherConfig struct {
	Name           string   `yaml:"name" json:"name"`                       // Publisher名称
	Type           string   `yaml:"type" json:"type"`                       // Publisher类型
	ServerURL      string   `yaml:"server_url" json:"server_url"`           // 服务器URL
	StreamName     string   `yaml:"stream_name" json:"stream_name"`         // 流名称
	StreamSubjects []string `yaml:"stream_subjects" json:"stream_subjects"` // 主题列表
}

// ConsumerConfig Consumer配置
type ConsumerConfig struct {
	Name         string `yaml:"name" json:"name"`                   // Consumer名称
	Type         string `yaml:"type" json:"type"`                   // Consumer类型
	ServerURL    string `yaml:"server_url" json:"server_url"`       // 服务器URL
	StreamName   string `yaml:"stream_name" json:"stream_name"`     // 流名称
	Subject      string `yaml:"subject" json:"subject"`             // 订阅主题
	ConsumerName string `yaml:"consumer_name" json:"consumer_name"` // 消费者名称
	MaxInFlight  int    `yaml:"max_in_flight" json:"max_in_flight"` // 最大并发处理数
	AckWait      int    `yaml:"ack_wait" json:"ack_wait"`           // 消息确认等待时间(秒)
}

// ToServiceOptions 转换为ServiceOptions
func (c *Config) ToServiceOptions() ServiceOptions {
	return ServiceOptions{
		ServerType: ServerType(c.Server.Type),
		ServerOpts: types.ServerOptions{
			Host:     c.Server.Host,
			Port:     c.Server.Port,
			StoreDir: c.Server.StoreDir,
			Timeout:  5 * time.Second,
		},
		AutoStart: c.Server.Enable,
	}
}

// ToPublisherOptions 转换为PublisherOptions
func (pc *PublisherConfig) ToPublisherOptions() types.PublisherOptions {
	return types.PublisherOptions{
		ServerURL:      pc.ServerURL,
		ConnectTimeout: 10 * time.Second,
		StreamName:     pc.StreamName,
		StreamSubjects: pc.StreamSubjects,
	}
}

// ToConsumerOptions 转换为ConsumerOptions
func (cc *ConsumerConfig) ToConsumerOptions(handler types.MessageHandler) types.ConsumerOptions {
	ackWait := time.Duration(cc.AckWait) * time.Second
	if ackWait == 0 {
		ackWait = 30 * time.Second
	}

	return types.ConsumerOptions{
		ServerURL:      cc.ServerURL,
		ConnectTimeout: 10 * time.Second,
		StreamName:     cc.StreamName,
		Subject:        cc.Subject,
		ConsumerName:   cc.ConsumerName,
		Handler:        handler,
		MaxInFlight:    cc.MaxInFlight,
		AckWait:        ackWait,
	}
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Enable:   true,
			Type:     "nats",
			Host:     "127.0.0.1",
			Port:     4222,
			StoreDir: "/tmp/msghub",
		},
		Publishers: []PublisherConfig{},
		Consumers:  []ConsumerConfig{},
	}
}
