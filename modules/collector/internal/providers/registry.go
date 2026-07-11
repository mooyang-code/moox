package providers

import (
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type Registry struct {
	mu        sync.RWMutex
	providers map[marketdata.ProviderID]KlineProvider
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[marketdata.ProviderID]KlineProvider)}
}
func (r *Registry) Register(provider KlineProvider) error {
	if provider == nil || provider.ID() == "" {
		return fmt.Errorf("provider id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[provider.ID()]; ok {
		return fmt.Errorf("provider %q is already registered", provider.ID())
	}
	r.providers[provider.ID()] = provider
	return nil
}
func (r *Registry) Kline(id marketdata.ProviderID, query CapabilityQuery) (KlineProvider, error) {
	r.mu.RLock()
	provider, ok := r.providers[id]
	r.mu.RUnlock()
	if !ok {
		return nil, NewError(ErrorUnsupported, "provider is not registered", nil)
	}
	for _, capability := range provider.Capabilities() {
		if capability.Matches(query) {
			return provider, nil
		}
	}
	return nil, NewError(ErrorUnsupported, "provider capability is unavailable", nil)
}
