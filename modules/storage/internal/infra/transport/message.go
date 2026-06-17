// Package transport 定义底层消息传输抽象和可替换实现。
package transport

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

// ProducerOptions 定义消息生产者配置。
type ProducerOptions struct {
	ServerURL      string        // 服务器URL地址
	ConnectTimeout time.Duration // 连接超时时间
	StreamName     string        // 消息流名称
	StreamSubjects []string      // 订阅主题列表
}
