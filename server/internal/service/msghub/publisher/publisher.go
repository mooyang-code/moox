package publisher

import (
	"fmt"

	"github.com/mooyang-code/moox/server/internal/service/msghub"
	"github.com/mooyang-code/moox/server/internal/service/msghub/publisher/registry"
	"github.com/mooyang-code/moox/server/internal/service/msghub/types"

	// 导入NATS实现以触发init注册
	_ "github.com/mooyang-code/moox/server/internal/service/msghub/publisher/nats"
)

// NewPublisher 创建发布器
func NewPublisher(publisherType msghub.PublisherType, opts types.PublisherOptions) (types.Publisher, error) {
	constructor, err := registry.GetPublisherConstructor(publisherType)
	if err != nil {
		return nil, err
	}

	publisher, err := constructor(opts)
	if err != nil {
		return nil, fmt.Errorf("创建发布器失败: %w", err)
	}

	return publisher, nil
}

// ListPublisherTypes 列出所有可用的发布器类型
func ListPublisherTypes() []msghub.PublisherType {
	return registry.ListPublisherTypes()
}
