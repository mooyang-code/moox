package collector

import (
	"fmt"
	"sync"
)

// CollectorDescriptor 采集器描述符
type CollectorDescriptor struct {
	Source      string    // 数据源，如 "binance"
	SourceCN    string    // 数据源中文名
	DataType    string    // 数据类型，如 "kline"
	DataTypeCN  string    // 数据类型中文名
	Description string    // 描述
	Collector   Collector // 采集器实例（无状态，可复用）
}

// CollectorRegistry 采集器注册中心
type CollectorRegistry struct {
	collectors map[string]*CollectorDescriptor
	mu         sync.RWMutex
}

var globalRegistry = &CollectorRegistry{
	collectors: make(map[string]*CollectorDescriptor),
}

// GetRegistry 获取全局注册表
func GetRegistry() *CollectorRegistry {
	return globalRegistry
}

// Register 注册采集器
func Register(descriptor *CollectorDescriptor) error {
	return globalRegistry.Register(descriptor)
}

// Register 注册采集器
func (r *CollectorRegistry) Register(descriptor *CollectorDescriptor) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := r.generateKey(descriptor.Source, descriptor.DataType)
	if _, exists := r.collectors[key]; exists {
		return fmt.Errorf("采集器 %s 已经注册", key)
	}

	r.collectors[key] = descriptor
	return nil
}

// Get 获取采集器
func (r *CollectorRegistry) Get(source, dataType string) (Collector, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := r.generateKey(source, dataType)
	descriptor, exists := r.collectors[key]
	if !exists {
		return nil, fmt.Errorf("采集器 %s 未注册", key)
	}
	return descriptor.Collector, nil
}

// GetDescriptor 获取采集器描述符
func (r *CollectorRegistry) GetDescriptor(source, dataType string) (*CollectorDescriptor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := r.generateKey(source, dataType)
	descriptor, exists := r.collectors[key]
	if !exists {
		return nil, fmt.Errorf("采集器 %s 未注册", key)
	}
	return descriptor, nil
}

// List 列出所有采集器描述符
func (r *CollectorRegistry) List() []*CollectorDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	descriptors := make([]*CollectorDescriptor, 0, len(r.collectors))
	for _, desc := range r.collectors {
		descriptors = append(descriptors, desc)
	}
	return descriptors
}

// ListBySource 按数据源列出采集器
func (r *CollectorRegistry) ListBySource(source string) []*CollectorDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var descriptors []*CollectorDescriptor
	for _, desc := range r.collectors {
		if desc.Source == source {
			descriptors = append(descriptors, desc)
		}
	}
	return descriptors
}

func (r *CollectorRegistry) generateKey(source, dataType string) string {
	return fmt.Sprintf("%s:%s", source, dataType)
}

// GetDataTypes 获取所有已注册的数据类型（去重）
func (r *CollectorRegistry) GetDataTypes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 使用 map 去重
	typeSet := make(map[string]struct{})
	for _, desc := range r.collectors {
		typeSet[desc.DataType] = struct{}{}
	}

	// 转换为切片
	var types []string
	for t := range typeSet {
		types = append(types, t)
	}
	return types
}

// CollectorBuilder 构建器模式，简化采集器注册
type CollectorBuilder struct {
	descriptor *CollectorDescriptor
}

// NewBuilder 创建构建器
func NewBuilder() *CollectorBuilder {
	return &CollectorBuilder{
		descriptor: &CollectorDescriptor{},
	}
}

// Source 设置数据源
func (b *CollectorBuilder) Source(source, sourceCN string) *CollectorBuilder {
	b.descriptor.Source = source
	b.descriptor.SourceCN = sourceCN
	return b
}

// DataType 设置数据类型
func (b *CollectorBuilder) DataType(dataType, dataTypeCN string) *CollectorBuilder {
	b.descriptor.DataType = dataType
	b.descriptor.DataTypeCN = dataTypeCN
	return b
}

// Description 设置描述
func (b *CollectorBuilder) Description(description string) *CollectorBuilder {
	b.descriptor.Description = description
	return b
}

// Collector 设置采集器实例
func (b *CollectorBuilder) Collector(collector Collector) *CollectorBuilder {
	b.descriptor.Collector = collector
	return b
}

// Register 注册采集器
func (b *CollectorBuilder) Register() error {
	return Register(b.descriptor)
}
