package stockcn

import (
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

type SessionSegment struct {
	Open  time.Duration
	Close time.Duration
}

type SessionSpec struct {
	Location *time.Location
	Segments []SessionSegment
}

func ChinaStockSession() SessionSpec {
	return SessionSpec{Location: time.FixedZone("Asia/Shanghai", 8*60*60), Segments: []SessionSegment{{Open: 9*time.Hour + 30*time.Minute, Close: 11*time.Hour + 30*time.Minute}, {Open: 13 * time.Hour, Close: 15 * time.Hour}}}
}

func (spec SessionSpec) Validate() error {
	if spec.Location == nil {
		return fmt.Errorf("session timezone is required")
	}
	if len(spec.Segments) == 0 {
		return fmt.Errorf("session segments are required")
	}
	for index, segment := range spec.Segments {
		if segment.Open < 0 || segment.Close <= segment.Open || segment.Close > 24*time.Hour {
			return fmt.Errorf("session segment %d is invalid", index)
		}
	}
	return nil
}

type TradabilityStatus string

const (
	Tradable              TradabilityStatus = "tradable"
	NonTradingDayStatus   TradabilityStatus = "non_trading_day"
	OutOfCalendarCoverage TradabilityStatus = "out_of_coverage"
	OutsideSession        TradabilityStatus = "outside_session"
)

type TradabilityPolicy struct {
	Calendar marketcalendar.TradingCalendar
	Session  SessionSpec
}

func (policy TradabilityPolicy) Status(at time.Time) (TradabilityStatus, error) {
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
		if errors.Is(err, marketcalendar.ErrOutOfCoverage) {
			return OutOfCalendarCoverage, err
		}
		return OutOfCalendarCoverage, err
	}
	if calendarStatus == marketcalendar.OutOfCoverage {
		return OutOfCalendarCoverage, nil
	}
	if calendarStatus == marketcalendar.NonTradingDay {
		return NonTradingDayStatus, nil
	}
	wallClock := time.Duration(local.Hour())*time.Hour + time.Duration(local.Minute())*time.Minute + time.Duration(local.Second())*time.Second
	for _, segment := range policy.Session.Segments {
		if wallClock >= segment.Open && wallClock < segment.Close {
			return Tradable, nil
		}
	}
	return OutsideSession, nil
}

func (policy TradabilityPolicy) ExpectedMinuteBuckets(date marketcalendar.CivilDate) ([]time.Time, error) {
	if err := policy.Session.Validate(); err != nil {
		return nil, err
	}
	status, err := policy.Calendar.Status(date)
	if err != nil {
		return nil, err
	}
	if status != marketcalendar.TradingDay {
		return []time.Time{}, nil
	}
	start := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, policy.Session.Location)
	result := make([]time.Time, 0, 240)
	for _, segment := range policy.Session.Segments {
		for offset := segment.Open; offset < segment.Close; offset += time.Minute {
			result = append(result, start.Add(offset))
		}
	}
	return result, nil
}
