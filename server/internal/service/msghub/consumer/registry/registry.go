package registry

import (
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/server/internal/service/msghub"
	"github.com/mooyang-code/moox/server/internal/service/msghub/types"
)

// CreateConsumer 创建消费者的函数类型
type CreateConsumer func(opts types.ConsumerOptions) (types.Consumer, error)

// 消费者注册表
var consumerRegistry struct {
	once     sync.Once
	registry map[msghub.ConsumerType]CreateConsumer
}

// 初始化消费者注册表
func initConsumerRegistry() {
	consumerRegistry.once.Do(func() {
		consumerRegistry.registry = make(map[msghub.ConsumerType]CreateConsumer)
	})
}

// RegisterConsumerType 注册消费者类型
func RegisterConsumerType(consumerType msghub.ConsumerType, constructor CreateConsumer) {
	initConsumerRegistry()
	consumerRegistry.registry[consumerType] = constructor
	fmt.Printf("消费者类型已注册: %s\n", consumerType)
}

// GetConsumerConstructor 获取消费者构造函数
func GetConsumerConstructor(consumerType msghub.ConsumerType) (CreateConsumer, error) {
	initConsumerRegistry()
	constructor, exists := consumerRegistry.registry[consumerType]
	if !exists {
		return nil, fmt.Errorf("未知的消费者类型: %s", consumerType)
	}
	return constructor, nil
}

// ListConsumerTypes 列出所有已注册的消费者类型
func ListConsumerTypes() []msghub.ConsumerType {
	initConsumerRegistry()
	var types []msghub.ConsumerType
	for t := range consumerRegistry.registry {
		types = append(types, t)
	}
	return types
}
