package msghub

import (
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/server/internal/service/msghub/types"
)

// publisherRegistry Publisher实例注册表
type publisherRegistry struct {
	mu         sync.RWMutex
	publishers map[string]types.Publisher
}

// newPublisherRegistry 创建Publisher注册表
func newPublisherRegistry() *publisherRegistry {
	return &publisherRegistry{
		publishers: make(map[string]types.Publisher),
	}
}

// Register 注册Publisher
func (r *publisherRegistry) Register(name string, pub types.Publisher) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.publishers[name]; exists {
		return fmt.Errorf("Publisher已存在: %s", name)
	}

	r.publishers[name] = pub
	return nil
}

// Get 获取Publisher
func (r *publisherRegistry) Get(name string) (types.Publisher, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	pub, exists := r.publishers[name]
	if !exists {
		return nil, fmt.Errorf("Publisher不存在: %s", name)
	}

	return pub, nil
}

// Unregister 注销Publisher
func (r *publisherRegistry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	pub, exists := r.publishers[name]
	if !exists {
		return fmt.Errorf("Publisher不存在: %s", name)
	}

	// 关闭Publisher
	if err := pub.Close(); err != nil {
		return fmt.Errorf("关闭Publisher失败: %w", err)
	}

	delete(r.publishers, name)
	return nil
}

// List 列出所有Publisher名称
func (r *publisherRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for name := range r.publishers {
		names = append(names, name)
	}
	return names
}

// CloseAll 关闭所有Publisher
func (r *publisherRegistry) CloseAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var lastErr error
	for name, pub := range r.publishers {
		if err := pub.Close(); err != nil {
			lastErr = fmt.Errorf("关闭Publisher失败 (%s): %w", name, err)
		}
	}

	r.publishers = make(map[string]types.Publisher)
	return lastErr
}

// consumerRegistry Consumer实例注册表
type consumerRegistry struct {
	mu        sync.RWMutex
	consumers map[string]types.Consumer
}

// newConsumerRegistry 创建Consumer注册表
func newConsumerRegistry() *consumerRegistry {
	return &consumerRegistry{
		consumers: make(map[string]types.Consumer),
	}
}

// Register 注册Consumer
func (r *consumerRegistry) Register(name string, consumer types.Consumer) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.consumers[name]; exists {
		return fmt.Errorf("Consumer已存在: %s", name)
	}

	r.consumers[name] = consumer
	return nil
}

// Get 获取Consumer
func (r *consumerRegistry) Get(name string) (types.Consumer, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	consumer, exists := r.consumers[name]
	if !exists {
		return nil, fmt.Errorf("Consumer不存在: %s", name)
	}

	return consumer, nil
}

// Unregister 注销Consumer
func (r *consumerRegistry) Unregister(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	consumer, exists := r.consumers[name]
	if !exists {
		return fmt.Errorf("Consumer不存在: %s", name)
	}

	// 停止Consumer
	if err := consumer.Stop(); err != nil {
		return fmt.Errorf("停止Consumer失败: %w", err)
	}

	delete(r.consumers, name)
	return nil
}

// List 列出所有Consumer名称
func (r *consumerRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for name := range r.consumers {
		names = append(names, name)
	}
	return names
}

// StopAll 停止所有Consumer
func (r *consumerRegistry) StopAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var lastErr error
	for name, consumer := range r.consumers {
		if err := consumer.Stop(); err != nil {
			lastErr = fmt.Errorf("停止Consumer失败 (%s): %w", name, err)
		}
	}

	r.consumers = make(map[string]types.Consumer)
	return lastErr
}
