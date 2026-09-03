package stockcn

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/log"
)

var (
	ErrCalendarExpiringSoon = errors.New("calendar coverage is expiring soon")
	ErrCalendarExpired      = errors.New("calendar coverage has expired")
	horizonWarningState     = struct {
		sync.Mutex
		lastDateByCoverage map[string]string
	}{lastDateByCoverage: make(map[string]string)}
)

type calendarFile struct {
	Timezone      string    `yaml:"timezone"`
	CoverageStart string    `yaml:"coverage_start"`
	CoverageEnd   string    `yaml:"coverage_end"`
	Sessions      []session `yaml:"sessions"`
	ClosedDates   []string  `yaml:"closed_dates"`
	OpenDates     []string  `yaml:"exceptional_open_dates"`
}

type session struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
}

type Session struct {
	Open  time.Time
	Close time.Time
}

type TradingDay struct {
	TradeDate string
	Sessions  []Session
}

type Calendar struct {
	location *time.Location
	start    time.Time
	end      time.Time
	sessions []session
	closed   map[string]struct{}
	open     map[string]struct{}
	static   *marketcalendar.TradingCalendar
}

func LoadCalendar(path string) (*Calendar, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file calendarFile
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(file.Timezone)
	if err != nil {
		return nil, err
	}
	start, err := time.ParseInLocation("2006-01-02", file.CoverageStart, location)
	if err != nil {
		return nil, err
	}
	end, err := time.ParseInLocation("2006-01-02", file.CoverageEnd, location)
	if err != nil {
		return nil, err
	}
	calendar := &Calendar{
		location: location,
		start:    start,
		end:      end,
		sessions: append([]session(nil), file.Sessions...),
		closed:   make(map[string]struct{}, len(file.ClosedDates)),
		open:     make(map[string]struct{}, len(file.OpenDates)),
	}
	static, err := marketcalendar.Load("cn_stock")
	if err != nil {
		return nil, fmt.Errorf("load embedded cn_stock calendar: %w", err)
	}
	calendar.static = &static
	for _, value := range file.ClosedDates {
		calendar.closed[value] = struct{}{}
	}
	for _, value := range file.OpenDates {
		calendar.open[value] = struct{}{}
	}
	return calendar, nil
}

func (c *Calendar) Location() *time.Location {
	if c == nil {
		return nil
	}
	return c.location
}

func (c *Calendar) IsOpen(at time.Time) bool {
	if c == nil || c.location == nil {
		return false
	}
	local := at.In(c.location)
	date := local.Format("2006-01-02")
	if !c.isTradingDay(date, local.Weekday()) {
		return false
	}
	minute := local.Format("15:04")
	for _, current := range c.sessions {
		if minute >= current.Start && minute < current.End {
			return true
		}
	}
	return false
}

func (c *Calendar) ValidateHorizon(now time.Time, minDays int) error {
	err := c.CheckHorizon(now, minDays)
	if errors.Is(err, ErrCalendarExpiringSoon) {
		localDate := now.In(c.location).Format("2006-01-02")
		coverageEnd := c.end.Format("2006-01-02")
		if shouldLogHorizonWarning(coverageEnd, localDate) {
			log.Warnf("stock_cn calendar horizon warning: %v", err)
		}
		return nil
	}
	return err
}

func shouldLogHorizonWarning(coverageEnd, localDate string) bool {
	horizonWarningState.Lock()
	defer horizonWarningState.Unlock()
	if horizonWarningState.lastDateByCoverage[coverageEnd] == localDate {
		return false
	}
	horizonWarningState.lastDateByCoverage[coverageEnd] = localDate
	return true
}

func (c *Calendar) CheckHorizon(now time.Time, minDays int) error {
	if c == nil || c.location == nil {
		return fmt.Errorf("calendar is not initialized")
	}
	local := now.In(c.location)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.location)
	if today.After(c.end) {
		return fmt.Errorf("%w: coverage ended on %s", ErrCalendarExpired, c.end.Format("2006-01-02"))
	}
	if c.end.Before(today.AddDate(0, 0, minDays)) {
		return fmt.Errorf("%w: coverage ends on %s", ErrCalendarExpiringSoon, c.end.Format("2006-01-02"))
	}
	return nil
}

func (c *Calendar) TradingDays(start, end time.Time) ([]TradingDay, error) {
	if c == nil || c.location == nil {
		return nil, fmt.Errorf("calendar is not initialized")
	}
	if !end.After(start) {
		return nil, fmt.Errorf("end must be after start")
	}
	day := time.Date(start.In(c.location).Year(), start.In(c.location).Month(), start.In(c.location).Day(), 0, 0, 0, 0, c.location)
	last := end.In(c.location)
	days := make([]TradingDay, 0)
	for day.Before(last) {
		date := day.Format("2006-01-02")
		if c.isTradingDay(date, day.Weekday()) {
			sessions := make([]Session, 0, len(c.sessions))
			for _, value := range c.sessions {
				openTime, err := time.ParseInLocation("2006-01-02 15:04", date+" "+value.Start, c.location)
				if err != nil {
					return nil, err
				}
				closeTime, err := time.ParseInLocation("2006-01-02 15:04", date+" "+value.End, c.location)
				if err != nil {
					return nil, err
				}
				sessions = append(sessions, Session{Open: openTime.UTC(), Close: closeTime.UTC()})
			}
			days = append(days, TradingDay{TradeDate: date, Sessions: sessions})
		}
		day = day.AddDate(0, 0, 1)
	}
	return days, nil
}

func (c *Calendar) ExpectedMinuteBars(tradeDate string) ([]time.Time, error) {
	if c == nil || c.location == nil {
		return nil, fmt.Errorf("calendar is not initialized")
	}
	day, err := time.ParseInLocation("2006-01-02", tradeDate, c.location)
	if err != nil {
		return nil, err
	}
	if !c.isTradingDay(tradeDate, day.Weekday()) {
		return nil, nil
	}
	bars := make([]time.Time, 0, 240)
	for _, current := range c.sessions {
		openTime, err := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+current.Start, c.location)
		if err != nil {
			return nil, err
		}
		closeTime, err := time.ParseInLocation("2006-01-02 15:04", tradeDate+" "+current.End, c.location)
		if err != nil {
			return nil, err
		}
		for cursor := openTime; cursor.Before(closeTime); cursor = cursor.Add(time.Minute) {
			bars = append(bars, cursor.UTC())
		}
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Before(bars[j]) })
	return bars, nil
}

// LookbackStart returns the first session of the Nth most recent trading day
// at or before now. It keeps history lookback semantics aligned with the
// exchange calendar instead of treating weekends and holidays as data days.
func (c *Calendar) LookbackStart(now time.Time, tradingDays int) (time.Time, error) {
	if c == nil || c.location == nil {
		return time.Time{}, fmt.Errorf("calendar is not initialized")
	}
	if tradingDays <= 0 {
		return time.Time{}, fmt.Errorf("trading days must be positive")
	}
	if len(c.sessions) == 0 {
		return time.Time{}, fmt.Errorf("calendar has no sessions")
	}
	local := now.In(c.location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.location)
	// Before the first session opens, the current trading date has not produced
	// any completed market data yet. Exclude it so a lookback never returns a
	// future session start.
	firstOpen, err := time.ParseInLocation("15:04", c.sessions[0].Start, c.location)
	if err != nil {
		return time.Time{}, err
	}
	if local.Hour() < firstOpen.Hour() || local.Hour() == firstOpen.Hour() && local.Minute() < firstOpen.Minute() {
		day = day.AddDate(0, 0, -1)
	}
	seen := 0
	for !day.Before(c.start) {
		date := day.Format("2006-01-02")
		if c.isTradingDay(date, day.Weekday()) {
			seen++
			if seen == tradingDays {
				openTime, err := time.ParseInLocation("2006-01-02 15:04", date+" "+c.sessions[0].Start, c.location)
				if err != nil {
					return time.Time{}, err
				}
				return openTime.UTC(), nil
			}
		}
		day = day.AddDate(0, 0, -1)
	}
	return time.Time{}, fmt.Errorf("calendar does not cover %d trading days before %s", tradingDays, local.Format("2006-01-02"))
}

// LatestClosedMinute returns the most recent closed 1m bucket and its end
// time. It walks backwards through the configured sessions, so a deployment
// canary can replay the last real market session on weekends and holidays
// without weakening the normal bounded-history policy.
func (c *Calendar) LatestClosedMinute(now time.Time, settleDelay time.Duration) (time.Time, time.Time, error) {
	if c == nil || c.location == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("calendar is not initialized")
	}
	if settleDelay < 0 {
		return time.Time{}, time.Time{}, fmt.Errorf("settle delay must not be negative")
	}
	local := now.In(c.location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.location)
	for date := day; !date.Before(c.start); date = date.AddDate(0, 0, -1) {
		dateString := date.Format("2006-01-02")
		if !c.isTradingDay(dateString, date.Weekday()) {
			continue
		}
		for sessionIndex := len(c.sessions) - 1; sessionIndex >= 0; sessionIndex-- {
			current := c.sessions[sessionIndex]
			openTime, err := time.ParseInLocation("2006-01-02 15:04", dateString+" "+current.Start, c.location)
			if err != nil {
				return time.Time{}, time.Time{}, err
			}
			closeTime, err := time.ParseInLocation("2006-01-02 15:04", dateString+" "+current.End, c.location)
			if err != nil {
				return time.Time{}, time.Time{}, err
			}
			candidate := closeTime.Add(-time.Minute)
			for !candidate.Before(openTime) {
				if !candidate.Add(time.Minute).Add(settleDelay).After(now) {
					return candidate.UTC(), candidate.Add(time.Minute).UTC(), nil
				}
				candidate = candidate.Add(-time.Minute)
			}
		}
	}
	return time.Time{}, time.Time{}, fmt.Errorf("calendar has no closed minute within lookback")
}

func (c *Calendar) isTradingDay(date string, weekday time.Weekday) bool {
	if c == nil {
		return false
	}
	day, err := time.ParseInLocation("2006-01-02", date, c.location)
	if err != nil || day.Before(c.start) || day.After(c.end) {
		return false
	}
	if _, ok := c.closed[date]; ok {
		return false
	}
	if _, ok := c.open[date]; ok {
		return true
	}
	if c.static != nil {
		if civil, err := marketcalendar.ParseCivilDate(date); err == nil {
			if status, statusErr := c.static.Status(civil); statusErr == nil {
				return status == marketcalendar.TradingDay
			}
			return false
		}
		return false
	}
	return weekday != time.Saturday && weekday != time.Sunday
}
