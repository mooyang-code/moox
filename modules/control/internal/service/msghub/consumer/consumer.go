package consumer

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/control/internal/service/msghub/consumer/registry"
	"github.com/mooyang-code/moox/modules/control/internal/service/msghub/types"

	// 导入NATS实现以触发init注册
	_ "github.com/mooyang-code/moox/modules/control/internal/service/msghub/consumer/nats"
)

// NewConsumer 创建消费者
func NewConsumer(consumerType types.ConsumerType, opts types.ConsumerOptions) (types.Consumer, error) {
	constructor, err := registry.GetConsumerConstructor(consumerType)
	if err != nil {
		return nil, err
	}

	consumer, err := constructor(opts)
	if err != nil {
		return nil, fmt.Errorf("创建消费者失败: %w", err)
	}

	return consumer, nil
}

// ListConsumerTypes 列出所有可用的消费者类型
func ListConsumerTypes() []types.ConsumerType {
	return registry.ListConsumerTypes()
}
