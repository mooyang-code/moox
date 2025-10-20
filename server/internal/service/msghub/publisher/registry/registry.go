package registry

import (
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/server/internal/service/msghub"
	"github.com/mooyang-code/moox/server/internal/service/msghub/types"
)

// CreatePublisher 创建发布器的函数类型
type CreatePublisher func(opts types.PublisherOptions) (types.Publisher, error)

// 发布器注册表
var publisherRegistry struct {
	once     sync.Once
	registry map[msghub.PublisherType]CreatePublisher
}

// 初始化发布器注册表
func initPublisherRegistry() {
	publisherRegistry.once.Do(func() {
		publisherRegistry.registry = make(map[msghub.PublisherType]CreatePublisher)
	})
}

// RegisterPublisherType 注册发布器类型
func RegisterPublisherType(publisherType msghub.PublisherType, constructor CreatePublisher) {
	initPublisherRegistry()
	publisherRegistry.registry[publisherType] = constructor
	fmt.Printf("发布器类型已注册: %s\n", publisherType)
}

// GetPublisherConstructor 获取发布器构造函数
func GetPublisherConstructor(publisherType msghub.PublisherType) (CreatePublisher, error) {
	initPublisherRegistry()
	constructor, exists := publisherRegistry.registry[publisherType]
	if !exists {
		return nil, fmt.Errorf("未知的发布器类型: %s", publisherType)
	}
	return constructor, nil
}

// ListPublisherTypes 列出所有已注册的发布器类型
func ListPublisherTypes() []msghub.PublisherType {
	initPublisherRegistry()
	types := make([]msghub.PublisherType, 0, len(publisherRegistry.registry))
	for t := range publisherRegistry.registry {
		types = append(types, t)
	}
	return types
}
