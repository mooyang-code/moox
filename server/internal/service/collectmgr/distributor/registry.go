package distributor

import (
	"sync"

	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"
)

// DistributorRegistry 分配器注册中心
type DistributorRegistry struct {
	distributors map[string]TaskDistributor
	mu           sync.RWMutex
}

// NewDistributorRegistry 创建分配器注册中心
func NewDistributorRegistry(nodeDAO cloudnodedao.CloudNodeDAO, symbolProvider SymbolProvider) *DistributorRegistry {
	registry := &DistributorRegistry{
		distributors: make(map[string]TaskDistributor),
	}

	// 创建基础分配器
	base := NewBaseDistributor(nodeDAO, symbolProvider)

	// 注册各类型分配器
	registry.Register(NewKlineDistributor(base))
	registry.Register(NewTickerDistributor(base))
	registry.Register(NewOrderbookDistributor(base))
	registry.Register(NewTradeDistributor(base))
	registry.Register(NewNewsDistributor(base))

	return registry
}

// Register 注册分配器
func (r *DistributorRegistry) Register(distributor TaskDistributor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.distributors[distributor.GetDataType()] = distributor
}

// Get 获取分配器
func (r *DistributorRegistry) Get(dataType string) TaskDistributor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.distributors[dataType]
}

// GetOrDefault 获取分配器，如果不存在则返回默认分配器
func (r *DistributorRegistry) GetOrDefault(dataType string) TaskDistributor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if distributor, ok := r.distributors[dataType]; ok {
		return distributor
	}

	// 返回 kline 分配器作为默认（按对象拆分）
	return r.distributors[model.DataTypeKline]
}

// List 列出所有已注册的数据类型
func (r *DistributorRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]string, 0, len(r.distributors))
	for dataType := range r.distributors {
		types = append(types, dataType)
	}
	return types
}
