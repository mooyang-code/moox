package markets

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/packages/marketmanifest"
)

// Descriptor is the immutable identity and runtime policy of one logical market.
// It deliberately does not expose a Provider as part of the market identity.
type Descriptor struct {
	MarketID       marketdata.MarketID
	SpaceID        string
	AssetClass     string
	Timezone       string
	RuntimeEnabled bool
}

// The policy interfaces are intentionally narrow. Individual market packages own
// their concrete policies; the registry only needs stable identity and lifecycle.
type UniversePolicy interface{}
type CalendarSession struct{ Open, Close time.Time }
type CalendarDay struct {
	ExchangeID marketdata.ExchangeID
	TradeDate  string
	Timezone   string
	Status     string
	Sessions   []CalendarSession
}
type CalendarPolicy interface {
	TradingDays(time.Time, time.Time) ([]CalendarDay, error)
}
type SymbolPolicy interface{}
type RoutingPolicy interface{}
type QualityPolicy interface{}
type CoveragePolicy interface{}

type Module interface {
	Descriptor() Descriptor
	Universe() UniversePolicy
	Calendar() CalendarPolicy
	Symbols() SymbolPolicy
	Routing() RoutingPolicy
	Quality() QualityPolicy
	Coverage() CoveragePolicy
}

func DescriptorFromManifest(manifest marketmanifest.Manifest) (Descriptor, error) {
	marketID := marketdata.MarketID(strings.TrimSpace(manifest.MarketID))
	if marketID == "" || strings.TrimSpace(manifest.SpaceID) == "" {
		return Descriptor{}, fmt.Errorf("market_id and space_id are required")
	}
	if string(marketID) != manifest.SpaceID {
		return Descriptor{}, fmt.Errorf("market_id %q and space_id %q must match", marketID, manifest.SpaceID)
	}
	return Descriptor{
		MarketID:       marketID,
		SpaceID:        manifest.SpaceID,
		AssetClass:     manifest.AssetClass,
		Timezone:       manifest.Timezone,
		RuntimeEnabled: manifest.RuntimeEnabled,
	}, nil
}
