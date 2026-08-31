package marketcalendar

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const civilDateLayout = "2006-01-02"

var (
	ErrUnknownCalendar      = errors.New("unknown trading calendar")
	ErrInvalidCivilDate     = errors.New("invalid civil date")
	ErrOutOfCoverage        = errors.New("date is outside calendar coverage")
	ErrInvalidRange         = errors.New("invalid calendar date range")
	ErrNoPreviousTradingDay = errors.New("no previous trading day in calendar coverage")
	ErrNoNextTradingDay     = errors.New("no next trading day in calendar coverage")
	ErrInvalidCalendarData  = errors.New("invalid trading calendar data")
	ErrInvalidManifest      = errors.New("invalid trading calendar manifest")
	ErrCalendarChecksum     = errors.New("trading calendar checksum mismatch")
	ErrCalendarExpiring     = errors.New("trading calendar is nearing valid_through")
	ErrCalendarExpired      = errors.New("trading calendar coverage has expired")
)

// CivilDate is a calendar date without a clock or location.
//
// Its fields are private on purpose: callers can only construct a validated
// value, so converting through a time zone can never change its date.
type CivilDate struct {
	year  int16
	month uint8
	day   uint8
}

// NewCivilDate constructs a validated civil date.
func NewCivilDate(year int, month time.Month, day int) (CivilDate, error) {
	if year < 1 || year > 9999 || month < time.January || month > time.December || day < 1 || day > 31 {
		return CivilDate{}, fmt.Errorf("%w: %04d-%02d-%02d", ErrInvalidCivilDate, year, month, day)
	}
	date := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || date.Month() != month || date.Day() != day {
		return CivilDate{}, fmt.Errorf("%w: %04d-%02d-%02d", ErrInvalidCivilDate, year, month, day)
	}
	return CivilDate{year: int16(year), month: uint8(month), day: uint8(day)}, nil
}

// ParseCivilDate parses the canonical YYYY-MM-DD representation.
func ParseCivilDate(value string) (CivilDate, error) {
	if len(value) != len(civilDateLayout) || value[4] != '-' || value[7] != '-' {
		return CivilDate{}, fmt.Errorf("%w: %q must use YYYY-MM-DD", ErrInvalidCivilDate, value)
	}
	for i, character := range value {
		if i == 4 || i == 7 {
			continue
		}
		if character < '0' || character > '9' {
			return CivilDate{}, fmt.Errorf("%w: %q must use YYYY-MM-DD", ErrInvalidCivilDate, value)
		}
	}
	year, _ := strconv.Atoi(value[:4])
	month, _ := strconv.Atoi(value[5:7])
	day, _ := strconv.Atoi(value[8:10])
	date, err := NewCivilDate(year, time.Month(month), day)
	if err != nil {
		return CivilDate{}, fmt.Errorf("%w: %q", ErrInvalidCivilDate, value)
	}
	return date, nil
}

// MustParseCivilDate parses value and panics if it is not a valid civil date.
func MustParseCivilDate(value string) CivilDate {
	date, err := ParseCivilDate(value)
	if err != nil {
		panic(err)
	}
	return date
}

// Validate reports whether d is a non-zero, valid civil date.
func (d CivilDate) Validate() error {
	if d.year < 1 || d.month < uint8(time.January) || d.month > uint8(time.December) || d.day < 1 {
		return fmt.Errorf("%w: %q", ErrInvalidCivilDate, d)
	}
	_, err := NewCivilDate(int(d.year), time.Month(d.month), int(d.day))
	return err
}

func (d CivilDate) IsZero() bool {
	return d == CivilDate{}
}

func (d CivilDate) Year() int {
	return int(d.year)
}

func (d CivilDate) Month() time.Month {
	return time.Month(d.month)
}

func (d CivilDate) Day() int {
	return int(d.day)
}

func (d CivilDate) String() string {
	if d.IsZero() {
		return "0000-00-00"
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.Year(), d.Month(), d.Day())
}

func (d CivilDate) GoString() string {
	return d.String()
}

func (d CivilDate) Before(other CivilDate) bool {
	return compareCivilDate(d, other) < 0
}

func (d CivilDate) After(other CivilDate) bool {
	return compareCivilDate(d, other) > 0
}

func (d CivilDate) Equal(other CivilDate) bool {
	return d == other
}

func (d CivilDate) MarshalText() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return []byte(d.String()), nil
}

func (d *CivilDate) UnmarshalText(value []byte) error {
	parsed, err := ParseCivilDate(string(value))
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

func (d CivilDate) MarshalJSON() ([]byte, error) {
	text, err := d.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

func (d *CivilDate) UnmarshalJSON(value []byte) error {
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return fmt.Errorf("%w: civil date must be a JSON string", ErrInvalidCivilDate)
	}
	return d.UnmarshalText([]byte(text))
}

type CoverageStatus uint8

const (
	TradingDay CoverageStatus = iota
	NonTradingDay
	OutOfCoverage
)

func (s CoverageStatus) String() string {
	switch s {
	case TradingDay:
		return "trading_day"
	case NonTradingDay:
		return "non_trading_day"
	case OutOfCoverage:
		return "out_of_coverage"
	default:
		return fmt.Sprintf("coverage_status(%d)", s)
	}
}

// Manifest describes the immutable data embedded in a TradingCalendar.
type Manifest struct {
	CalendarID   string    `json:"calendar_id"`
	Source       string    `json:"source"`
	Version      int       `json:"version"`
	DataVersion  string    `json:"data_version,omitempty"`
	ValidFrom    CivilDate `json:"valid_from"`
	ValidThrough CivilDate `json:"valid_through"`
	SHA256       string    `json:"sha256"`
}

type calendarManifest struct {
	CalendarID   string `json:"calendar_id"`
	Source       string `json:"source"`
	Version      int    `json:"version"`
	DataVersion  string `json:"data_version"`
	ValidFrom    string `json:"valid_from"`
	ValidThrough string `json:"valid_through"`
	SHA256       string `json:"sha256"`
}

type calendarFile struct {
	CalendarID  string   `json:"calendar_id"`
	TradingDays []string `json:"trading_days"`
}

type calendarData struct {
	id            string
	manifest      Manifest
	tradingDays   []CivilDate
	tradingDaySet map[CivilDate]struct{}
}

// TradingCalendar is an immutable view over an embedded trading calendar.
type TradingCalendar struct {
	data *calendarData
}

//go:embed data/cn_trading_days.json
var embeddedTradingDays []byte

//go:embed data/manifest.json
var embeddedManifest []byte

// Load loads one of the built-in calendars by ID.
func Load(id string) (TradingCalendar, error) {
	if id != "cn_stock" {
		return TradingCalendar{}, fmt.Errorf("%w: %q", ErrUnknownCalendar, id)
	}
	return loadCalendar(embeddedTradingDays, embeddedManifest)
}

func (c TradingCalendar) ID() string {
	if c.data == nil {
		return ""
	}
	return c.data.id
}

// Manifest returns a value copy of the calendar manifest.
func (c TradingCalendar) Manifest() Manifest {
	if c.data == nil {
		return Manifest{}
	}
	return c.data.manifest
}

func (c TradingCalendar) FirstDate() CivilDate {
	if c.data == nil || len(c.data.tradingDays) == 0 {
		return CivilDate{}
	}
	return c.data.tradingDays[0]
}

func (c TradingCalendar) LastDate() CivilDate {
	if c.data == nil || len(c.data.tradingDays) == 0 {
		return CivilDate{}
	}
	return c.data.tradingDays[len(c.data.tradingDays)-1]
}

// Status classifies date without assuming that an unknown date is a holiday.
func (c TradingCalendar) Status(date CivilDate) (CoverageStatus, error) {
	if err := c.checkCovered(date); err != nil {
		return OutOfCoverage, err
	}
	if _, ok := c.data.tradingDaySet[date]; ok {
		return TradingDay, nil
	}
	return NonTradingDay, nil
}

// Readiness checks whether the embedded data is safe to use for the supplied
// civil date. It fails closed after valid_through and reports an expiring
// calendar inside warningWindow so the annual update job can run before the
// first uncovered trading day.
func (c TradingCalendar) Readiness(asOf CivilDate, warningWindow time.Duration) error {
	if c.data == nil {
		return fmt.Errorf("%w: calendar is not loaded", ErrCalendarExpired)
	}
	if err := asOf.Validate(); err != nil {
		return err
	}
	if asOf.Before(c.FirstDate()) {
		return fmt.Errorf("%w: %s is before %s", ErrOutOfCoverage, asOf, c.FirstDate())
	}
	if asOf.After(c.LastDate()) {
		return fmt.Errorf("%w: %s is after %s", ErrCalendarExpired, asOf, c.LastDate())
	}
	if warningWindow <= 0 {
		return nil
	}
	warningDays := int((warningWindow + 24*time.Hour - 1) / (24 * time.Hour))
	threshold, ok := shiftCivilDate(c.LastDate(), -warningDays)
	if ok && !asOf.Before(threshold) {
		return fmt.Errorf("%w: valid_through=%s", ErrCalendarExpiring, c.LastDate())
	}
	return nil
}

// PrevTradingDay returns the closest trading day strictly before date.
func (c TradingCalendar) PrevTradingDay(date CivilDate) (CivilDate, error) {
	if err := c.checkCovered(date); err != nil {
		return CivilDate{}, err
	}
	for candidate := date; ; {
		previous, ok := shiftCivilDate(candidate, -1)
		if !ok || previous.Before(c.FirstDate()) {
			return CivilDate{}, fmt.Errorf("%w: %s", ErrNoPreviousTradingDay, date)
		}
		if _, ok := c.data.tradingDaySet[previous]; ok {
			return previous, nil
		}
		candidate = previous
	}
}

// NextTradingDay returns the closest trading day strictly after date.
func (c TradingCalendar) NextTradingDay(date CivilDate) (CivilDate, error) {
	if err := c.checkCovered(date); err != nil {
		return CivilDate{}, err
	}
	for candidate := date; ; {
		next, ok := shiftCivilDate(candidate, 1)
		if !ok || next.After(c.LastDate()) {
			return CivilDate{}, fmt.Errorf("%w: %s", ErrNoNextTradingDay, date)
		}
		if _, ok := c.data.tradingDaySet[next]; ok {
			return next, nil
		}
		candidate = next
	}
}

// TradingDays returns all trading days in the inclusive [start, end] range.
func (c TradingCalendar) TradingDays(start, end CivilDate) ([]CivilDate, error) {
	if err := c.checkCovered(start); err != nil {
		return nil, err
	}
	if err := c.checkCovered(end); err != nil {
		return nil, err
	}
	if start.After(end) {
		return nil, fmt.Errorf("%w: %s is after %s", ErrInvalidRange, start, end)
	}
	first := lowerBound(c.data.tradingDays, start)
	last := upperBound(c.data.tradingDays, end)
	result := make([]CivilDate, last-first)
	copy(result, c.data.tradingDays[first:last])
	return result, nil
}

func (c TradingCalendar) checkCovered(date CivilDate) error {
	if c.data == nil {
		return fmt.Errorf("%w: calendar is not loaded", ErrOutOfCoverage)
	}
	if err := date.Validate(); err != nil {
		return err
	}
	if date.Before(c.FirstDate()) || date.After(c.LastDate()) {
		return fmt.Errorf("%w: %s is outside %s..%s", ErrOutOfCoverage, date, c.FirstDate(), c.LastDate())
	}
	return nil
}

func loadCalendar(dataRaw, manifestRaw []byte) (TradingCalendar, error) {
	data, err := parseCalendarData(dataRaw)
	if err != nil {
		return TradingCalendar{}, err
	}
	manifest, err := parseCalendarManifest(manifestRaw)
	if err != nil {
		return TradingCalendar{}, err
	}
	if manifest.CalendarID != data.calendarID {
		return TradingCalendar{}, fmt.Errorf("%w: manifest calendar_id %q does not match data calendar_id %q", ErrInvalidManifest, manifest.CalendarID, data.calendarID)
	}
	if manifest.ValidFrom != data.dates[0].String() || manifest.ValidThrough != data.dates[len(data.dates)-1].String() {
		return TradingCalendar{}, fmt.Errorf("%w: manifest coverage does not match data boundaries", ErrInvalidManifest)
	}
	sum := sha256.Sum256(dataRaw)
	wantHash := "sha256:" + hex.EncodeToString(sum[:])
	if manifest.SHA256 != wantHash {
		return TradingCalendar{}, fmt.Errorf("%w: got %q, want %q", ErrCalendarChecksum, manifest.SHA256, wantHash)
	}
	validFrom, _ := ParseCivilDate(manifest.ValidFrom)
	validThrough, _ := ParseCivilDate(manifest.ValidThrough)
	return TradingCalendar{data: &calendarData{
		id: manifest.CalendarID,
		manifest: Manifest{
			CalendarID:   manifest.CalendarID,
			Source:       manifest.Source,
			Version:      manifest.Version,
			DataVersion:  manifest.DataVersion,
			ValidFrom:    validFrom,
			ValidThrough: validThrough,
			SHA256:       manifest.SHA256,
		},
		tradingDays:   append([]CivilDate(nil), data.dates...),
		tradingDaySet: data.set,
	}}, nil
}

type parsedCalendarData struct {
	calendarID string
	dates      []CivilDate
	set        map[CivilDate]struct{}
}

func parseCalendarData(raw []byte) (parsedCalendarData, error) {
	var file calendarFile
	if err := decodeJSON(raw, &file); err != nil {
		return parsedCalendarData{}, fmt.Errorf("%w: decode data: %v", ErrInvalidCalendarData, err)
	}
	if strings.TrimSpace(file.CalendarID) == "" {
		return parsedCalendarData{}, fmt.Errorf("%w: calendar_id is required", ErrInvalidCalendarData)
	}
	if len(file.TradingDays) == 0 {
		return parsedCalendarData{}, fmt.Errorf("%w: trading_days is empty", ErrInvalidCalendarData)
	}
	dates := make([]CivilDate, 0, len(file.TradingDays))
	set := make(map[CivilDate]struct{}, len(file.TradingDays))
	for index, text := range file.TradingDays {
		date, err := ParseCivilDate(text)
		if err != nil {
			return parsedCalendarData{}, fmt.Errorf("%w: trading_days[%d] %q: %v", ErrInvalidCalendarData, index, text, err)
		}
		if _, exists := set[date]; exists {
			return parsedCalendarData{}, fmt.Errorf("%w: duplicate trading date %q", ErrInvalidCalendarData, text)
		}
		if len(dates) > 0 && !dates[len(dates)-1].Before(date) {
			return parsedCalendarData{}, fmt.Errorf("%w: trading_days[%d] %q is not strictly after %q", ErrInvalidCalendarData, index, text, dates[len(dates)-1])
		}
		dates = append(dates, date)
		set[date] = struct{}{}
	}
	return parsedCalendarData{calendarID: file.CalendarID, dates: dates, set: set}, nil
}

func parseCalendarManifest(raw []byte) (calendarManifest, error) {
	var manifest calendarManifest
	if err := decodeJSON(raw, &manifest); err != nil {
		return calendarManifest{}, fmt.Errorf("%w: decode manifest: %v", ErrInvalidManifest, err)
	}
	if strings.TrimSpace(manifest.CalendarID) == "" || strings.TrimSpace(manifest.Source) == "" {
		return calendarManifest{}, fmt.Errorf("%w: calendar_id and source are required", ErrInvalidManifest)
	}
	if manifest.Version <= 0 {
		return calendarManifest{}, fmt.Errorf("%w: version must be positive", ErrInvalidManifest)
	}
	from, err := ParseCivilDate(manifest.ValidFrom)
	if err != nil {
		return calendarManifest{}, fmt.Errorf("%w: valid_from: %v", ErrInvalidManifest, err)
	}
	through, err := ParseCivilDate(manifest.ValidThrough)
	if err != nil {
		return calendarManifest{}, fmt.Errorf("%w: valid_through: %v", ErrInvalidManifest, err)
	}
	if from.After(through) {
		return calendarManifest{}, fmt.Errorf("%w: valid_from is after valid_through", ErrInvalidManifest)
	}
	if !isSHA256(manifest.SHA256) {
		return calendarManifest{}, fmt.Errorf("%w: sha256 must use sha256:<64 lowercase hex>", ErrInvalidManifest)
	}
	return manifest, nil
}

func decodeJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func compareCivilDate(left, right CivilDate) int {
	if left.year != right.year {
		if left.year < right.year {
			return -1
		}
		return 1
	}
	if left.month != right.month {
		if left.month < right.month {
			return -1
		}
		return 1
	}
	if left.day < right.day {
		return -1
	}
	if left.day > right.day {
		return 1
	}
	return 0
}

func shiftCivilDate(date CivilDate, days int) (CivilDate, bool) {
	if err := date.Validate(); err != nil {
		return CivilDate{}, false
	}
	shifted := time.Date(date.Year(), date.Month(), date.Day()+days, 0, 0, 0, 0, time.UTC)
	if shifted.Year() < 1 || shifted.Year() > 9999 {
		return CivilDate{}, false
	}
	result, err := NewCivilDate(shifted.Year(), shifted.Month(), shifted.Day())
	return result, err == nil
}

func lowerBound(dates []CivilDate, target CivilDate) int {
	left, right := 0, len(dates)
	for left < right {
		middle := left + (right-left)/2
		if dates[middle].Before(target) {
			left = middle + 1
		} else {
			right = middle
		}
	}
	return left
}

func upperBound(dates []CivilDate, target CivilDate) int {
	left, right := 0, len(dates)
	for left < right {
		middle := left + (right-left)/2
		if dates[middle].After(target) {
			right = middle
		} else {
			left = middle + 1
		}
	}
	return left
}
