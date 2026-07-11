package markets

import (
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type Registry struct {
	mu      sync.RWMutex
	modules map[marketdata.MarketID]Module
}

func NewRegistry() *Registry { return &Registry{modules: make(map[marketdata.MarketID]Module)} }

func (r *Registry) Register(module Module) error {
	if module == nil {
		return fmt.Errorf("market module is required")
	}
	descriptor := module.Descriptor()
	if descriptor.MarketID == "" || descriptor.SpaceID == "" {
		return fmt.Errorf("market module descriptor is incomplete")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.modules[descriptor.MarketID]; exists {
		return fmt.Errorf("market module %q is already registered", descriptor.MarketID)
	}
	r.modules[descriptor.MarketID] = module
	return nil
}

func (r *Registry) Lookup(marketID marketdata.MarketID) (Module, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	module, ok := r.modules[marketID]
	return module, ok
}

func (r *Registry) All() []Module {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Module, 0, len(r.modules))
	for _, module := range r.modules {
		result = append(result, module)
	}
	return result
}
