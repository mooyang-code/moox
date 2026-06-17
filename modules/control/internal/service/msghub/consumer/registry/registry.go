package registry

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/control/internal/service/msghub/types"
	"sync"
)

// CreateConsumer 创建消费者的函数类型
type CreateConsumer func(opts types.ConsumerOptions) (types.Consumer, error)

// 消费者注册表
var consumerRegistry struct {
	once     sync.Once
	registry map[types.ConsumerType]CreateConsumer
}

// 初始化消费者注册表
func initConsumerRegistry() {
	consumerRegistry.once.Do(func() {
		consumerRegistry.registry = make(map[types.ConsumerType]CreateConsumer)
	})
}

// RegisterConsumerType 注册消费者类型
func RegisterConsumerType(consumerType types.ConsumerType, constructor CreateConsumer) {
	initConsumerRegistry()
	consumerRegistry.registry[consumerType] = constructor
	fmt.Printf("消费者类型已注册: %s\n", consumerType)
}

// GetConsumerConstructor 获取消费者构造函数
func GetConsumerConstructor(consumerType types.ConsumerType) (CreateConsumer, error) {
	initConsumerRegistry()
	constructor, exists := consumerRegistry.registry[consumerType]
	if !exists {
		return nil, fmt.Errorf("未知的消费者类型: %s", consumerType)
	}
	return constructor, nil
}

// ListConsumerTypes 列出所有已注册的消费者类型
func ListConsumerTypes() []types.ConsumerType {
	initConsumerRegistry()
	var types []types.ConsumerType
	for t := range consumerRegistry.registry {
		types = append(types, t)
	}
	return types
}
