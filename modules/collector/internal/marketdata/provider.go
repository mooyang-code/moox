package marketdata

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/packages/routeprobe"
)

// SourceKey identifies one concrete data access channel under a provider.
// ProviderID alone is intentionally insufficient because one provider may
// expose several protocol variants or endpoint families.
type SourceKey struct {
	ProviderID string
	SourceID   string
}

func (k SourceKey) normalized() SourceKey {
	return SourceKey{
		ProviderID: normalizeID(k.ProviderID),
		SourceID:   normalizeID(k.SourceID),
	}
}

func (k SourceKey) String() string {
	k = k.normalized()
	return k.ProviderID + "/" + k.SourceID
}

// ProtocolVariant distinguishes wire-compatible-looking sources that do not
// actually share framing, handshake, or authentication semantics.
type ProtocolVariant string

const (
	ProtocolHTTP       ProtocolVariant = "http"
	ProtocolTDXNormal  ProtocolVariant = "tdx_normal"
	ProtocolTDXClassic ProtocolVariant = "tdx_ex_classic"
	ProtocolTDXMac     ProtocolVariant = "tdx_ex_mac"
)

// ProviderDescriptor describes a concrete source, not merely a vendor name.
type ProviderDescriptor struct {
	ProviderID      string
	SourceID        string
	ProtocolVariant ProtocolVariant
	Transport       string
	Host            string
	Port            int
	Markets         []string
	InstrumentTypes []string
	Frequencies     []string
}

// RouteIPProvider supplies already-probed addresses for one request. The
// source adapter remains responsible for preserving the logical Host/SNI.
type RouteIPProvider interface {
	SelectRouteIPs(context.Context, routeprobe.SourceKey, routeprobe.Transport, string, int) ([]string, error)
}

func (d ProviderDescriptor) Key() SourceKey {
	return SourceKey{ProviderID: d.ProviderID, SourceID: d.SourceID}.normalized()
}

func (d ProviderDescriptor) Validate() error {
	if normalizeID(d.ProviderID) == "" {
		return fmt.Errorf("provider_id is required")
	}
	if normalizeID(d.SourceID) == "" {
		return fmt.Errorf("source_id is required")
	}
	if strings.TrimSpace(string(d.ProtocolVariant)) == "" {
		return fmt.Errorf("protocol_variant is required for %s", d.Key())
	}
	if strings.TrimSpace(d.Transport) == "" {
		return fmt.Errorf("transport is required for %s", d.Key())
	}
	if d.Port < 1 || d.Port > 65535 {
		return fmt.Errorf("port %d is invalid for %s", d.Port, d.Key())
	}
	if len(d.Markets) == 0 || len(d.InstrumentTypes) == 0 {
		return fmt.Errorf("markets and instrument_types are required for %s", d.Key())
	}
	for _, market := range append(append([]string(nil), d.Markets...), d.InstrumentTypes...) {
		if normalizeID(market) == "" {
			return fmt.Errorf("support ranges must not contain empty values for %s", d.Key())
		}
	}
	if len(d.Frequencies) == 0 {
		return fmt.Errorf("frequencies are required for %s", d.Key())
	}
	for _, frequency := range d.Frequencies {
		if strings.TrimSpace(frequency) == "" {
			return fmt.Errorf("frequencies must not contain empty values for %s", d.Key())
		}
	}
	return nil
}

func (d ProviderDescriptor) SupportsMarketInstrument(marketID, instrumentType string) bool {
	return contains(d.Markets, marketID) && contains(d.InstrumentTypes, instrumentType)
}

// Registry is a process-local immutable-after-registration source registry.
// Registration is explicit and duplicate keys are errors; silent replacement
// would make a deployment depend on init order.
type Registry struct {
	mu      sync.RWMutex
	sources map[SourceKey]ProviderDescriptor
}

func NewRegistry() *Registry {
	return &Registry{sources: make(map[SourceKey]ProviderDescriptor)}
}

func (r *Registry) Register(descriptor ProviderDescriptor) error {
	if r == nil {
		return fmt.Errorf("provider registry is nil")
	}
	if err := descriptor.Validate(); err != nil {
		return err
	}
	key := descriptor.Key()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sources[key]; exists {
		return fmt.Errorf("source %s is already registered", key)
	}
	r.sources[key] = cloneDescriptor(descriptor)
	return nil
}

func (r *Registry) Lookup(key SourceKey) (ProviderDescriptor, bool) {
	if r == nil {
		return ProviderDescriptor{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptor, ok := r.sources[key.normalized()]
	if !ok {
		return ProviderDescriptor{}, false
	}
	return cloneDescriptor(descriptor), true
}

func (r *Registry) List() []ProviderDescriptor {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]ProviderDescriptor, 0, len(r.sources))
	for _, descriptor := range r.sources {
		result = append(result, cloneDescriptor(descriptor))
	}
	return result
}

func cloneDescriptor(in ProviderDescriptor) ProviderDescriptor {
	in.Markets = append([]string(nil), in.Markets...)
	in.InstrumentTypes = append([]string(nil), in.InstrumentTypes...)
	in.Frequencies = append([]string(nil), in.Frequencies...)
	return in
}

func normalizeID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
