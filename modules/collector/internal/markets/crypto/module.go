package crypto

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"time"
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
func (m *Module) Calendar() markets.CalendarPolicy {
	return CalendarPolicy{TwentyFourSeven: true, ExchangeID: m.exchange}
}
func (*Module) Symbols() markets.SymbolPolicy    { return SymbolPolicy{} }
func (*Module) Routing() markets.RoutingPolicy   { return RoutingPolicy{} }
func (*Module) Quality() markets.QualityPolicy   { return QualityPolicy{PriceTolerance: "0.000001"} }
func (*Module) Coverage() markets.CoveragePolicy { return CoveragePolicy{OverlapBuckets: 2} }

type UniversePolicy struct{}
type CalendarPolicy struct {
	TwentyFourSeven bool
	ExchangeID      marketdata.ExchangeID
}
type SymbolPolicy struct{}
type RoutingPolicy struct{}
type QualityPolicy struct{ PriceTolerance string }
type CoveragePolicy struct{ OverlapBuckets int }

func (p CalendarPolicy) TradingDays(start, end time.Time) ([]markets.CalendarDay, error) {
	if !p.TwentyFourSeven || p.ExchangeID == "" {
		return nil, fmt.Errorf("24x7 calendar and exchange are required")
	}
	start = start.UTC()
	end = end.UTC()
	if !end.After(start) {
		return nil, fmt.Errorf("end must be after start")
	}
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	result := make([]markets.CalendarDay, 0)
	for day.Before(end) {
		closeTime := day.Add(24 * time.Hour)
		result = append(result, markets.CalendarDay{ExchangeID: p.ExchangeID, TradeDate: day.Format("2006-01-02"), Timezone: "UTC", Status: "open", Sessions: []markets.CalendarSession{{Open: day, Close: closeTime}}})
		day = closeTime
	}
	return result, nil
}
