package crypto

import (
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

type Module struct {
	marketID marketdata.MarketID
	exchange marketdata.ExchangeID
}

func New(marketID marketdata.MarketID, exchange marketdata.ExchangeID) *Module {
	return &Module{marketID: marketID, exchange: exchange}
}
func (m *Module) Descriptor() markets.Descriptor {
	return markets.Descriptor{MarketID: m.marketID, SpaceID: string(m.marketID), AssetClass: "crypto", Timezone: "UTC"}
}
func (*Module) Universe() markets.UniversePolicy { return UniversePolicy{} }
func (*Module) Calendar() markets.CalendarPolicy { return CalendarPolicy{TwentyFourSeven: true} }
func (*Module) Symbols() markets.SymbolPolicy    { return SymbolPolicy{} }
func (*Module) Routing() markets.RoutingPolicy   { return RoutingPolicy{} }
func (*Module) Quality() markets.QualityPolicy   { return QualityPolicy{PriceTolerance: "0.000001"} }
func (*Module) Coverage() markets.CoveragePolicy { return CoveragePolicy{OverlapBuckets: 2} }

type UniversePolicy struct{}
type CalendarPolicy struct{ TwentyFourSeven bool }
type SymbolPolicy struct{}
type RoutingPolicy struct{}
type QualityPolicy struct{ PriceTolerance string }
type CoveragePolicy struct{ OverlapBuckets int }
