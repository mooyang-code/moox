package stockcn

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/markets"
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
type Calendar struct {
	location     *time.Location
	start, end   time.Time
	sessions     []session
	closed, open map[string]struct{}
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
	calendar := &Calendar{location: location, start: start, end: end, sessions: file.Sessions, closed: make(map[string]struct{}), open: make(map[string]struct{})}
	for _, date := range file.ClosedDates {
		calendar.closed[date] = struct{}{}
	}
	for _, date := range file.OpenDates {
		calendar.open[date] = struct{}{}
	}
	return calendar, nil
}
func (c *Calendar) IsOpen(at time.Time) bool {
	local := at.In(c.location)
	date := local.Format("2006-01-02")
	day, _ := time.ParseInLocation("2006-01-02", date, c.location)
	if day.Before(c.start) || day.After(c.end) {
		return false
	}
	if _, ok := c.closed[date]; ok {
		return false
	}
	_, exception := c.open[date]
	if !exception && (local.Weekday() == time.Saturday || local.Weekday() == time.Sunday) {
		return false
	}
	clock := local.Format("15:04")
	for _, session := range c.sessions {
		if clock >= session.Start && clock < session.End {
			return true
		}
	}
	return false
}
func (c *Calendar) ValidateHorizon(now time.Time, minDays int) error {
	if c.end.Before(now.In(c.location).AddDate(0, 0, minDays)) {
		return fmt.Errorf("calendar coverage ends before required horizon")
	}
	return nil
}

func (c *Calendar) TradingDays(start, end time.Time) ([]markets.CalendarDay, error) {
	if c == nil || c.location == nil {
		return nil, fmt.Errorf("calendar is not initialized")
	}
	if !end.After(start) {
		return nil, fmt.Errorf("end must be after start")
	}
	day := time.Date(start.In(c.location).Year(), start.In(c.location).Month(), start.In(c.location).Day(), 0, 0, 0, 0, c.location)
	last := end.In(c.location)
	result := make([]markets.CalendarDay, 0)
	for day.Before(last) {
		date := day.Format("2006-01-02")
		_, forcedOpen := c.open[date]
		_, forcedClosed := c.closed[date]
		weekdayOpen := day.Weekday() != time.Saturday && day.Weekday() != time.Sunday
		if !day.Before(c.start) && !day.After(c.end) && !forcedClosed && (weekdayOpen || forcedOpen) {
			sessions := make([]markets.CalendarSession, 0, len(c.sessions))
			for _, value := range c.sessions {
				openTime, err := time.ParseInLocation("2006-01-02 15:04", date+" "+value.Start, c.location)
				if err != nil {
					return nil, err
				}
				closeTime, err := time.ParseInLocation("2006-01-02 15:04", date+" "+value.End, c.location)
				if err != nil {
					return nil, err
				}
				sessions = append(sessions, markets.CalendarSession{Open: openTime.UTC(), Close: closeTime.UTC()})
			}
			result = append(result, markets.CalendarDay{ExchangeID: "SSE", TradeDate: date, Timezone: c.location.String(), Status: "open", Sessions: sessions})
		}
		day = day.AddDate(0, 0, 1)
	}
	return result, nil
}
