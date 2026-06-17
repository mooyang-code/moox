package transport

import (
	"fmt"
	"sync"
)

// ProducerKind 表示底层消息传输实现类型。
type ProducerKind string

const (
	// ProducerKindNATS 表示 NATS/JetStream 生产者。
	ProducerKindNATS ProducerKind = "nats"
)

// ProducerConstructor 创建消息生产者的工厂函数类型。
type ProducerConstructor func(opts ProducerOptions) (Producer, error)

var producerRegistry struct {
	once     sync.Once
	registry map[ProducerKind]ProducerConstructor
}

func initProducerRegistry() {
	producerRegistry.once.Do(func() {
		producerRegistry.registry = make(map[ProducerKind]ProducerConstructor)
	})
}

// RegisterProducerKind 注册底层消息生产者类型。
func RegisterProducerKind(kind ProducerKind, constructor ProducerConstructor) {
	initProducerRegistry()
	producerRegistry.registry[kind] = constructor
}

// NewProducer 根据类型创建消息生产者。
func NewProducer(kind ProducerKind, opts ProducerOptions) (Producer, error) {
	initProducerRegistry()

	constructor, exists := producerRegistry.registry[kind]
	if !exists {
		return nil, fmt.Errorf("无效的消息生产者类型: %s", kind)
	}

	producer, err := constructor(opts)
	if err != nil {
		return nil, fmt.Errorf("创建消息生产者失败: %w", err)
	}
	return producer, nil
}
