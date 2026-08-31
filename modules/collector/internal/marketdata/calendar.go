package marketdata

import "github.com/mooyang-code/moox/packages/marketcalendar"

// CivilDate and CoverageStatus are aliases of the public calendar contract.
// Keeping one value type prevents a calendar loaded by the public package from
// being converted through a time.Time or a second, weaker representation.
type CivilDate = marketcalendar.CivilDate
type CoverageStatus = marketcalendar.CoverageStatus

const (
	TradingDay    = marketcalendar.TradingDay
	NonTradingDay = marketcalendar.NonTradingDay
	OutOfCoverage = marketcalendar.OutOfCoverage
)

type Calendar interface {
	ID() string
	FirstDate() CivilDate
	LastDate() CivilDate
	Status(CivilDate) (CoverageStatus, error)
}
