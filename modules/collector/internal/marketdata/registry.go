package marketdata

import (
	"fmt"
	"strings"
	"sync"
)

type Registry struct {
	mu        sync.RWMutex
	providers map[string]MarketProvider
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]MarketProvider)}
}

func (r *Registry) Register(provider MarketProvider) error {
	if provider == nil {
		return fmt.Errorf("%w: provider is nil", ErrInvalidRequest)
	}
	descriptor := provider.Descriptor()
	if err := descriptor.Validate(); err != nil {
		return err
	}
	id := strings.TrimSpace(descriptor.ID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[id]; ok {
		return ErrProviderAlreadyRegistered
	}
	r.providers[id] = provider
	return nil
}

func (r *Registry) Provider(id string) (MarketProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[strings.TrimSpace(id)]
	if !ok {
		return nil, ErrProviderNotFound
	}
	return provider, nil
}

func (r *Registry) KlineFetcher(id string) (KlineFetcher, error) {
	provider, err := r.Provider(id)
	if err != nil {
		return nil, err
	}
	fetcher, ok := provider.(KlineFetcher)
	if !ok {
		return nil, ErrFetcherNotSupported
	}
	return fetcher, nil
}

func (r *Registry) InstrumentFetcher(id string) (InstrumentFetcher, error) {
	provider, err := r.Provider(id)
	if err != nil {
		return nil, err
	}
	fetcher, ok := provider.(InstrumentFetcher)
	if !ok {
		return nil, ErrFetcherNotSupported
	}
	return fetcher, nil
}
