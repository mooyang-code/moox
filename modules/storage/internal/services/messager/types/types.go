// Package types 定义消息系统的核心数据类型和接口，包括消息结构和配置选项
package types

import (
	"time"
)

// Message 定义要发布的消息结构
type Message struct {
	Subject string    // 消息主题
	Data    []byte    // 消息数据
	ID      string    // 消息唯一标识
	Time    time.Time // 消息创建时间
}

// PublisherOptions 定义消息发布器的配置选项
type PublisherOptions struct {
	ServerURL      string        // 服务器URL地址
	ConnectTimeout time.Duration // 连接超时时间
	StreamName     string        // 消息流名称
	StreamSubjects []string      // 订阅主题列表
}
