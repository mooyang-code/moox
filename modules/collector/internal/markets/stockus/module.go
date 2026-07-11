package stockus

import "github.com/mooyang-code/moox/modules/collector/internal/markets"

type Module struct{}

func New() *Module { return &Module{} }
func (*Module) Descriptor() markets.Descriptor {
	return markets.Descriptor{MarketID: "stock_us", SpaceID: "stock_us", AssetClass: "stock", Timezone: "America/New_York", RuntimeEnabled: false}
}
func (*Module) Universe() markets.UniversePolicy { return nil }
func (*Module) Calendar() markets.CalendarPolicy { return nil }
func (*Module) Symbols() markets.SymbolPolicy    { return nil }
func (*Module) Routing() markets.RoutingPolicy   { return nil }
func (*Module) Quality() markets.QualityPolicy   { return nil }
func (*Module) Coverage() markets.CoveragePolicy { return nil }
