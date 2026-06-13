// Package registry 提供消息发布器的注册表管理，支持动态注册和获取不同类型的消息发布器
package registry

import (
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/messager/types"
)

// CreatePublisher 创建消息发布器的工厂函数类型
type CreatePublisher func(opts types.PublisherOptions) (types.Publisher, error)

// 消息发布器注册表
var publisherRegistry struct {
	once     sync.Once
	registry map[constants.PublisherType]CreatePublisher
}

// 初始化注册表
func initPublisherRegistry() {
	publisherRegistry.once.Do(func() {
		publisherRegistry.registry = make(map[constants.PublisherType]CreatePublisher)
	})
}

// RegisterPublisherType 注册发布器类型到系统
func RegisterPublisherType(publisherType constants.PublisherType, constructor CreatePublisher) {
	initPublisherRegistry()
	publisherRegistry.registry[publisherType] = constructor
}

// GetPublisher 消息发布器工厂函数
func GetPublisher(publisherType constants.PublisherType, opts types.PublisherOptions) (types.Publisher, error) {
	initPublisherRegistry()

	// 尝试根据发布器类型获取构造函数
	constructor, exists := publisherRegistry.registry[publisherType]
	if !exists {
		return nil, fmt.Errorf("无效的发布器类型: %s", publisherType)
	}

	// 调用构造函数创建发布器实例
	publisher, err := constructor(opts)
	if err != nil {
		return nil, fmt.Errorf("创建发布器实例失败: %v", err)
	}
	return publisher, nil
}
