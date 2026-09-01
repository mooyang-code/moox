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

// SourceStatus is the runtime activation state declared by the market
// manifest. Catalog-only and shadow sources may be registered for inspection,
// but the pipeline must not write their output into a canonical dataset.
type SourceStatus string

const (
	SourceEnabled     SourceStatus = "enabled"
	SourceShadow      SourceStatus = "shadow"
	SourceCatalogOnly SourceStatus = "catalog_only"
)

func (status SourceStatus) IsEnabled() bool {
	return status == "" || status == SourceEnabled
}

// ProviderDescriptor describes a concrete source, not merely a vendor name.
type ProviderDescriptor struct {
	ProviderID      string
	SourceID        string
	Status          SourceStatus
	ProtocolVariant ProtocolVariant
	Transport       string
	Host            string
	Port            int
	Markets         []string
	InstrumentTypes []string
	Frequencies     []string
}

// SourceSpec is the static support declaration for one concrete source. It is
// intentionally separate from runtime route health and contains no limiter or
// fleet-wide budget settings.
type SourceSpec struct {
	Key             SourceKey
	Status          SourceStatus
	ProtocolVariant ProtocolVariant
	Transport       string
	Host            string
	Port            int
	Markets         []string
	Instruments     []string
	Frequencies     []string
	TimestampMode   string
	CompleteOHLCV   bool
	HasAmount       bool
}

func (spec SourceSpec) Validate() error {
	if spec.Key.normalized().ProviderID == "" || spec.Key.normalized().SourceID == "" {
		return fmt.Errorf("provider_id and source_id are required")
	}
	if strings.TrimSpace(string(spec.ProtocolVariant)) == "" {
		return fmt.Errorf("protocol_variant is required for %s", spec.Key)
	}
	if strings.TrimSpace(spec.Transport) == "" {
		return fmt.Errorf("transport is required for %s", spec.Key)
	}
	switch spec.Status {
	case "", SourceEnabled, SourceShadow, SourceCatalogOnly:
	default:
		return fmt.Errorf("status %q is invalid for %s", spec.Status, spec.Key)
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return fmt.Errorf("port %d is invalid for %s", spec.Port, spec.Key)
	}
	if len(spec.Markets) == 0 || len(spec.Instruments) == 0 || len(spec.Frequencies) == 0 {
		return fmt.Errorf("markets, instruments and frequencies are required for %s", spec.Key)
	}
	for _, value := range append(append(append([]string(nil), spec.Markets...), spec.Instruments...), spec.Frequencies...) {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("source support values must not be empty for %s", spec.Key)
		}
	}
	if spec.TimestampMode != "" && spec.TimestampMode != string(TimestampStartLabel) && spec.TimestampMode != string(TimestampEndLabel) {
		return fmt.Errorf("timestamp mode %q is invalid for %s", spec.TimestampMode, spec.Key)
	}
	return nil
}

// SourceSpec returns the descriptor and its KlineSpec as one static source
// declaration. Passing the KlineSpec is required so timestamp and field
// capabilities cannot be silently dropped during readiness inspection.
func (d ProviderDescriptor) SourceSpec(kline KlineSpec) SourceSpec {
	return SourceSpec{
		Key: SourceKey{ProviderID: d.ProviderID, SourceID: d.SourceID}, Status: d.Status,
		ProtocolVariant: d.ProtocolVariant, Transport: d.Transport, Host: d.Host, Port: d.Port,
		Markets: append([]string(nil), d.Markets...), Instruments: append([]string(nil), d.InstrumentTypes...), Frequencies: append([]string(nil), d.Frequencies...),
		TimestampMode: kline.TimestampMode, CompleteOHLCV: kline.CompleteOHLCV, HasAmount: kline.HasAmount,
	}
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
	switch d.Status {
	case "", SourceEnabled, SourceShadow, SourceCatalogOnly:
	default:
		return fmt.Errorf("status %q is invalid for %s", d.Status, d.Key())
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
