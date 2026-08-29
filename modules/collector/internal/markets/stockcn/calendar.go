package stockcn

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
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
	if c == nil || c.location == nil {
		return fmt.Errorf("calendar is not initialized")
	}
	if c.end.Before(now.In(c.location).AddDate(0, 0, minDays)) {
		return fmt.Errorf("calendar coverage ends before required horizon")
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
	return weekday != time.Saturday && weekday != time.Sunday
}
