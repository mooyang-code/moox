package stockcn

import (
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

// China stock sessions use the shared MarketData session and tradability
// contract so index, equity and convertible-bond policies cannot drift apart.
type SessionSegment = marketdata.SessionSegment
type SessionSpec = marketdata.SessionSpec
type TradabilityStatus = marketdata.TradabilityStatus
type TradabilityPolicy = marketdata.TradabilityPolicy

const (
	Tradable              = marketdata.Tradable
	NonTradingDayStatus   = marketdata.NonTradingDayStatus
	OutOfCalendarCoverage = marketdata.OutOfCalendarCoverage
	OutsideSession        = marketdata.OutsideSession
)

func ChinaStockSession() SessionSpec {
	return SessionSpec{
		Location: time.FixedZone("Asia/Shanghai", 8*60*60),
		Segments: []SessionSegment{
			{Open: 9*time.Hour + 30*time.Minute, Close: 11*time.Hour + 30*time.Minute},
			{Open: 13 * time.Hour, Close: 15 * time.Hour},
		},
	}
}
