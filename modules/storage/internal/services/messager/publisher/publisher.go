// Package publisher 提供消息发布器的工厂函数和类型别名，支持多种消息组件的统一接口
package publisher

import (
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	_ "github.com/mooyang-code/moox/modules/storage/internal/services/messager/publisher/nats" // 使用具体的消息组件要初始化
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/publisher/registry"
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/types"
)

// 为了向后兼容，定义类型别名
type Message = types.Message
type PublisherOptions = types.PublisherOptions
type Publisher = types.Publisher

// NewPublisher 消息发布器工厂函数
func NewPublisher(publisherType constants.PublisherType, opts types.PublisherOptions) (types.Publisher, error) {
	return registry.GetPublisher(publisherType, opts)
}
