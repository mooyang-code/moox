package msghub

import (
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/msghub/types"
	"github.com/rs/xid"
)

// GenerateMessageID 生成消息ID
func GenerateMessageID() string {
	return xid.New().String()
}

// NewMessage 创建新消息
func NewMessage(subject string, data []byte) *types.Message {
	return &types.Message{
		ID:      GenerateMessageID(),
		Subject: subject,
		Data:    data,
		Headers: make(map[string]string),
		Time:    time.Now(),
	}
}

// NewMessageWithID 创建带ID的消息
func NewMessageWithID(id, subject string, data []byte) *types.Message {
	return &types.Message{
		ID:      id,
		Subject: subject,
		Data:    data,
		Headers: make(map[string]string),
		Time:    time.Now(),
	}
}
