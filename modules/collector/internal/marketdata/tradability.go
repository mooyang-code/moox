package marketdata

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

type TradabilityStatus string

const (
	Tradable              TradabilityStatus = "tradable"
	Auction               TradabilityStatus = "auction"
	PreMarket             TradabilityStatus = "pre_market"
	PostMarket            TradabilityStatus = "post_market"
	NonTradingDayStatus   TradabilityStatus = "non_trading_day"
	OutOfCalendarCoverage TradabilityStatus = "out_of_coverage"
	OutsideSession        TradabilityStatus = "outside_session"
)

type TradabilityPolicy struct {
	Calendar Calendar
	Session  SessionSpec
}

func (policy TradabilityPolicy) Status(at time.Time) (TradabilityStatus, error) {
	if policy.Calendar == nil {
		return OutsideSession, fmt.Errorf("trading calendar is required")
	}
	if err := policy.Session.Validate(); err != nil {
		return OutsideSession, err
	}
	local := at.In(policy.Session.Location)
	date, err := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return OutOfCalendarCoverage, err
	}
	calendarStatus, err := policy.Calendar.Status(date)
	if err != nil {
		return OutOfCalendarCoverage, err
	}
	if calendarStatus == OutOfCoverage {
		return OutOfCalendarCoverage, nil
	}
	if calendarStatus == NonTradingDay {
		return NonTradingDayStatus, nil
	}
	wallClock := time.Duration(local.Hour())*time.Hour + time.Duration(local.Minute())*time.Minute + time.Duration(local.Second())*time.Second
	for _, segment := range policy.Session.Segments {
		if wallClock < segment.Open || wallClock >= segment.Close {
			continue
		}
		switch segment.Kind {
		case SessionAuction:
			return Auction, nil
		case SessionPreMarket:
			return PreMarket, nil
		case SessionPostMarket:
			return PostMarket, nil
		default:
			return Tradable, nil
		}
	}
	return OutsideSession, nil
}

func (policy TradabilityPolicy) ExpectedMinuteBuckets(date CivilDate) ([]time.Time, error) {
	if policy.Calendar == nil {
		return nil, fmt.Errorf("trading calendar is required")
	}
	status, err := policy.Calendar.Status(date)
	if err != nil {
		return nil, err
	}
	if status == OutOfCoverage {
		return nil, fmt.Errorf("%w: calendar %s does not cover %s", marketcalendar.ErrOutOfCoverage, policy.Calendar.ID(), date)
	}
	if status != TradingDay {
		return []time.Time{}, nil
	}
	return policy.Session.ExpectedMinuteBuckets(date)
}
