package sources

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

// ProviderRegistration binds a source descriptor to the operations it
// actually implements. SourceKey, rather than ProviderID alone, is the
// registry identity so protocol variants cannot be selected accidentally.
type ProviderRegistration struct {
	Descriptor  marketdata.ProviderDescriptor
	Klines      marketdata.KlineFetcher
	Instruments marketdata.InstrumentFetcher
}

func (registration ProviderRegistration) Validate() error {
	if err := registration.Descriptor.Validate(); err != nil {
		return err
	}
	if registration.Klines == nil && registration.Instruments == nil {
		return fmt.Errorf("source %s has no fetcher", registration.Descriptor.Key())
	}
	if registration.Klines != nil {
		if registration.Klines.Descriptor().Key() != registration.Descriptor.Key() {
			return fmt.Errorf("kline fetcher descriptor %s does not match registration %s", registration.Klines.Descriptor().Key(), registration.Descriptor.Key())
		}
		if err := registration.Klines.KlineSpec().Validate(); err != nil {
			return fmt.Errorf("source %s kline spec: %w", registration.Descriptor.Key(), err)
		}
		spec := registration.Klines.KlineSpec()
		markets := spec.MarketIDs
		if len(markets) == 0 {
			markets = []string{spec.MarketID}
		}
		instruments := spec.InstrumentTypes
		if len(instruments) == 0 {
			instruments = []string{spec.InstrumentType}
		}
		for _, marketID := range markets {
			for _, instrumentType := range instruments {
				if !registration.Descriptor.SupportsMarketInstrument(marketID, instrumentType) {
					return fmt.Errorf("source %s descriptor does not support kline spec %s/%s", registration.Descriptor.Key(), marketID, instrumentType)
				}
			}
		}
		for _, frequency := range spec.Frequencies {
			found := false
			for _, declared := range registration.Descriptor.Frequencies {
				if declared == frequency {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("source %s descriptor does not declare frequency %q", registration.Descriptor.Key(), frequency)
			}
		}
	}
	if registration.Instruments != nil {
		if registration.Instruments.Descriptor().Key() != registration.Descriptor.Key() {
			return fmt.Errorf("instrument fetcher descriptor %s does not match registration %s", registration.Instruments.Descriptor().Key(), registration.Descriptor.Key())
		}
		if err := registration.Instruments.InstrumentSpec().Validate(); err != nil {
			return fmt.Errorf("source %s instrument spec: %w", registration.Descriptor.Key(), err)
		}
		spec := registration.Instruments.InstrumentSpec()
		markets := spec.MarketIDs
		if len(markets) == 0 {
			markets = []string{spec.MarketID}
		}
		instruments := spec.InstrumentTypes
		if len(instruments) == 0 {
			instruments = []string{spec.InstrumentType}
		}
		for _, marketID := range markets {
			for _, instrumentType := range instruments {
				if !registration.Descriptor.SupportsMarketInstrument(marketID, instrumentType) {
					return fmt.Errorf("source %s descriptor does not support instrument spec %s/%s", registration.Descriptor.Key(), marketID, instrumentType)
				}
			}
		}
	}
	return nil
}

type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[marketdata.SourceKey]ProviderRegistration
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: make(map[marketdata.SourceKey]ProviderRegistration)}
}

func (registry *ProviderRegistry) Register(registration ProviderRegistration) error {
	if registry == nil {
		return fmt.Errorf("provider registry is nil")
	}
	if err := registration.Validate(); err != nil {
		return err
	}
	key := registration.Descriptor.Key()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.providers[key]; exists {
		return fmt.Errorf("source %s is already registered", key)
	}
	registry.providers[key] = registration
	return nil
}

func (registry *ProviderRegistry) Lookup(key marketdata.SourceKey) (ProviderRegistration, bool) {
	if registry == nil {
		return ProviderRegistration{}, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	registration, ok := registry.providers[key]
	return registration, ok
}

func (registry *ProviderRegistry) List() []ProviderRegistration {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	keys := make([]marketdata.SourceKey, 0, len(registry.providers))
	for key := range registry.providers {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool { return keys[left].String() < keys[right].String() })
	result := make([]ProviderRegistration, 0, len(keys))
	for _, key := range keys {
		result = append(result, registry.providers[key])
	}
	return result
}

func (registry *ProviderRegistry) Kline(ctx context.Context, key marketdata.SourceKey, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	registration, ok := registry.Lookup(key)
	if !ok {
		return nil, fmt.Errorf("source %s is not registered", key)
	}
	if registration.Klines == nil {
		return nil, fmt.Errorf("source %s does not implement kline", key)
	}
	return registration.Klines.FetchKlines(ctx, request)
}
