package planner

import (
	"sync"

	cloudnodedao "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/model"
)

// PlannerRegistry 规划器注册中心
type PlannerRegistry struct {
	planners map[string]TaskPlanner
	mu       sync.RWMutex
}

// NewPlannerRegistry 创建规划器注册中心
func NewPlannerRegistry(nodeDAO cloudnodedao.CloudNodeDAO, symbolProvider SymbolProvider, onlineNodeIDProvider OnlineNodeIDsProvider) *PlannerRegistry {
	registry := &PlannerRegistry{
		planners: make(map[string]TaskPlanner),
	}

	// 创建基础规划器
	base := NewBasePlanner(nodeDAO, symbolProvider, onlineNodeIDProvider)

	// 注册各类型规划器
	registry.Register(NewKlinePlanner(base))
	registry.Register(NewTickerPlanner(base))
	registry.Register(NewOrderbookPlanner(base))
	registry.Register(NewTradePlanner(base))
	registry.Register(NewNewsPlanner(base))
	registry.Register(NewSymbolPlanner(base))

	return registry
}

// Register 注册规划器
func (r *PlannerRegistry) Register(planner TaskPlanner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.planners[planner.GetDataType()] = planner
}

// Get 获取规划器
func (r *PlannerRegistry) Get(dataType string) TaskPlanner {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.planners[dataType]
}

// GetOrDefault 获取规划器，如果不存在则返回默认规划器
func (r *PlannerRegistry) GetOrDefault(dataType string) TaskPlanner {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if planner, ok := r.planners[dataType]; ok {
		return planner
	}

	// 返回 kline 规划器作为默认（按对象拆分）
	return r.planners[model.DataTypeKline]
}

// List 列出所有已注册的数据类型
func (r *PlannerRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var types []string
	for dataType := range r.planners {
		types = append(types, dataType)
	}
	return types
}
