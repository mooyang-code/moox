package marketdata

import (
	"fmt"
	"strings"
	"sync"
)

type Registry struct {
	mu              sync.RWMutex
	providers       map[string]MarketProvider
	providerSources map[string][]string
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]MarketProvider), providerSources: make(map[string][]string)}
}

func (r *Registry) Register(provider MarketProvider) error {
	if provider == nil {
		return fmt.Errorf("%w: provider is nil", ErrInvalidRequest)
	}
	descriptor := provider.Descriptor()
	if err := descriptor.Validate(); err != nil {
		return err
	}
	key := descriptor.SourceKey()
	key.ProviderID = strings.ToLower(strings.TrimSpace(key.ProviderID))
	key.SourceID = strings.ToLower(strings.TrimSpace(key.SourceID))
	keyString := key.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[keyString]; ok {
		return ErrProviderAlreadyRegistered
	}
	r.providers[keyString] = provider
	providerID := strings.TrimSpace(key.ProviderID)
	r.providerSources[providerID] = append(r.providerSources[providerID], keyString)
	return nil
}

func (r *Registry) Provider(id string) (MarketProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id = strings.ToLower(strings.TrimSpace(id))
	if provider, ok := r.providers[id]; ok {
		return provider, nil
	}
	keys := r.providerSources[id]
	if len(keys) == 0 {
		return nil, ErrProviderNotFound
	}
	if len(keys) > 1 {
		return nil, ErrProviderAmbiguous
	}
	return r.providers[keys[0]], nil
}

func (r *Registry) Source(key SourceKey) (MarketProvider, error) {
	if err := key.Validate(); err != nil {
		return nil, err
	}
	key.ProviderID = strings.ToLower(strings.TrimSpace(key.ProviderID))
	key.SourceID = strings.ToLower(strings.TrimSpace(key.SourceID))
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[key.String()]
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

func (r *Registry) KlineFetcherBySource(key SourceKey) (KlineFetcher, error) {
	provider, err := r.Source(key)
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

func (r *Registry) InstrumentFetcherBySource(key SourceKey) (InstrumentFetcher, error) {
	provider, err := r.Source(key)
	if err != nil {
		return nil, err
	}
	fetcher, ok := provider.(InstrumentFetcher)
	if !ok {
		return nil, ErrFetcherNotSupported
	}
	return fetcher, nil
}
