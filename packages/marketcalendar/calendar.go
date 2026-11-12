// Package marketcalendar 提供内嵌的交易日历（A 股 cn_stock、港股 hk_stock、美股 us_stock）：按日期判断交易日、前后推交易日、查提前收市日。
//
// 每份日历两个文件：data/<日历 ID>.json（交易日与提前收市日）和 data/<日历 ID>.manifest.json（来源、覆盖期、校验和），
// 各自更新，互不影响。
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const civilDateLayout = "2006-01-02"

var (
	ErrUnknownCalendar      = errors.New("未知的交易日历")
	ErrInvalidCivilDate     = errors.New("日期无效")
	ErrOutOfCoverage        = errors.New("日期超出交易日历的覆盖范围")
	ErrNoPreviousTradingDay = errors.New("交易日历覆盖范围内没有更早的交易日")
	ErrNoNextTradingDay     = errors.New("交易日历覆盖范围内没有更晚的交易日")
	ErrInvalidCalendarData  = errors.New("交易日历数据无效")
	ErrInvalidManifest      = errors.New("交易日历清单无效")
	ErrCalendarChecksum     = errors.New("交易日历校验和不一致")
	// ErrNotTradingDay 表示给定日期在日历覆盖范围内，但不是交易日。
	ErrNotTradingDay = errors.New("不是交易日")
)

// CivilDate 是不带时刻与时区的日历日期。字段刻意不导出：只能构造经过校验的值，经时区换算也不会改变日期。
type CivilDate struct {
	year  int16
	month uint8
	day   uint8
}

// NewCivilDate 构造经过校验的日期。
func NewCivilDate(year int, month time.Month, day int) (CivilDate, error) {
	if year < 1 || year > 9999 || month < time.January || month > time.December || day < 1 || day > 31 {
		return CivilDate{}, fmt.Errorf("%w：%04d-%02d-%02d", ErrInvalidCivilDate, year, month, day)
	}
	date := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || date.Month() != month || date.Day() != day {
		return CivilDate{}, fmt.Errorf("%w：%04d-%02d-%02d", ErrInvalidCivilDate, year, month, day)
	}
	return CivilDate{year: int16(year), month: uint8(month), day: uint8(day)}, nil
}

// ParseCivilDate 解析规范的 YYYY-MM-DD 写法。
func ParseCivilDate(value string) (CivilDate, error) {
	if len(value) != len(civilDateLayout) || value[4] != '-' || value[7] != '-' {
		return CivilDate{}, fmt.Errorf("%w：%q 必须写成 YYYY-MM-DD", ErrInvalidCivilDate, value)
	}
	for i, character := range value {
		if i == 4 || i == 7 {
			continue
		}
		if character < '0' || character > '9' {
			return CivilDate{}, fmt.Errorf("%w：%q 必须写成 YYYY-MM-DD", ErrInvalidCivilDate, value)
		}
	}
	year, _ := strconv.Atoi(value[:4])
	month, _ := strconv.Atoi(value[5:7])
	day, _ := strconv.Atoi(value[8:10])
	date, err := NewCivilDate(year, time.Month(month), day)
	if err != nil {
		return CivilDate{}, fmt.Errorf("%w：%q", ErrInvalidCivilDate, value)
	}
	return date, nil
}

// Validate 检查 d 是非零的有效日期。
func (d CivilDate) Validate() error {
	if d.year < 1 || d.month < uint8(time.January) || d.month > uint8(time.December) || d.day < 1 {
		return fmt.Errorf("%w：%q", ErrInvalidCivilDate, d)
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

func (d CivilDate) Before(other CivilDate) bool {
	return compareCivilDate(d, other) < 0
}

func (d CivilDate) After(other CivilDate) bool {
	return compareCivilDate(d, other) > 0
}

type CoverageStatus uint8

const (
	TradingDay CoverageStatus = iota
	NonTradingDay
	OutOfCoverage
)

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
	// EarlyCloses 是提前收市日（半日市）：日期 → 当天的收市时刻（本地时间 HH:MM）。没有半日市的日历不写这一项。
	EarlyCloses map[string]string `json:"early_closes,omitempty"`
}

type calendarData struct {
	calendarID    string
	tradingDays   []CivilDate
	tradingDaySet map[CivilDate]struct{}
	// earlyCloses 是提前收市日当天的收市时刻（距本地零点的时长）；earlyCloseDates 是这些日期的升序列表。
	earlyCloses     map[CivilDate]time.Duration
	earlyCloseDates []CivilDate
}

// TradingCalendar 是内嵌交易日历的不可变视图。
type TradingCalendar struct {
	data *calendarData
}

//go:embed data/cn_stock.json
var embeddedTradingDays []byte

//go:embed data/cn_stock.manifest.json
var embeddedManifest []byte

//go:embed data/hk_stock.json
var embeddedHKTradingDays []byte

//go:embed data/hk_stock.manifest.json
var embeddedHKManifest []byte

//go:embed data/us_stock.json
var embeddedUSTradingDays []byte

//go:embed data/us_stock.manifest.json
var embeddedUSManifest []byte

// embeddedCalendar 是一份内嵌日历和它的加载结果。
type embeddedCalendar struct {
	data, manifest []byte
	once           sync.Once
	calendar       TradingCalendar
	err            error
}

var embeddedCalendars = map[string]*embeddedCalendar{
	"cn_stock": {data: embeddedTradingDays, manifest: embeddedManifest},
	"hk_stock": {data: embeddedHKTradingDays, manifest: embeddedHKManifest},
	"us_stock": {data: embeddedUSTradingDays, manifest: embeddedUSManifest},
}

// Load 按 ID 加载内嵌日历，未知的 ID 报错。内嵌日历不可变，每份的解析与校验（cn_stock 约 1 毫秒、1.5 MB）只做一次，
// 之后的调用复用同一份数据。
func Load(id string) (TradingCalendar, error) {
	entry, ok := embeddedCalendars[id]
	if !ok {
		return TradingCalendar{}, fmt.Errorf("%w：%q", ErrUnknownCalendar, id)
	}
	entry.once.Do(func() {
		entry.calendar, entry.err = loadCalendar(entry.data, entry.manifest)
		if entry.err == nil && entry.calendar.data.calendarID != id {
			entry.err = fmt.Errorf("%w：日历 %q 的数据文件写的是 %q", ErrInvalidCalendarData, id, entry.calendar.data.calendarID)
		}
	})
	return entry.calendar, entry.err
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

// Status 判断日期是否交易日；覆盖范围之外的日期返回 OutOfCoverage 与错误，不当作休市日。
func (c TradingCalendar) Status(date CivilDate) (CoverageStatus, error) {
	if err := c.checkCovered(date); err != nil {
		return OutOfCoverage, err
	}
	if _, ok := c.data.tradingDaySet[date]; ok {
		return TradingDay, nil
	}
	return NonTradingDay, nil
}

// PrevTradingDay 返回严格早于 date 的最近一个交易日。
func (c TradingCalendar) PrevTradingDay(date CivilDate) (CivilDate, error) {
	if err := c.checkCovered(date); err != nil {
		return CivilDate{}, err
	}
	for candidate := date; ; {
		previous, ok := shiftCivilDate(candidate, -1)
		if !ok || previous.Before(c.FirstDate()) {
			return CivilDate{}, fmt.Errorf("%w：%s", ErrNoPreviousTradingDay, date)
		}
		if _, ok := c.data.tradingDaySet[previous]; ok {
			return previous, nil
		}
		candidate = previous
	}
}

// NextTradingDay 返回严格晚于 date 的最近一个交易日。
func (c TradingCalendar) NextTradingDay(date CivilDate) (CivilDate, error) {
	if err := c.checkCovered(date); err != nil {
		return CivilDate{}, err
	}
	for candidate := date; ; {
		next, ok := shiftCivilDate(candidate, 1)
		if !ok || next.After(c.LastDate()) {
			return CivilDate{}, fmt.Errorf("%w：%s", ErrNoNextTradingDay, date)
		}
		if _, ok := c.data.tradingDaySet[next]; ok {
			return next, nil
		}
		candidate = next
	}
}

// EarlyClose 返回提前收市日（半日市）当天的收市时刻，用距本地零点的时长表示；不是提前收市日时 ok 为假。
func (c TradingCalendar) EarlyClose(date CivilDate) (time.Duration, bool) {
	if c.data == nil {
		return 0, false
	}
	closeAt, ok := c.data.earlyCloses[date]
	return closeAt, ok
}

// EarlyCloseDates 返回全部提前收市日，按日期升序；没有半日市的日历返回空。
func (c TradingCalendar) EarlyCloseDates() []CivilDate {
	if c.data == nil {
		return nil
	}
	return append([]CivilDate(nil), c.data.earlyCloseDates...)
}

// TradingDayIndex 返回交易日在日历中的序号（首个交易日为 0），不复制交易日列表；非交易日返回 ErrNotTradingDay。
func (c TradingCalendar) TradingDayIndex(date CivilDate) (int, error) {
	if err := c.checkCovered(date); err != nil {
		return 0, err
	}
	index := lowerBound(c.data.tradingDays, date)
	if index >= len(c.data.tradingDays) || c.data.tradingDays[index] != date {
		return 0, fmt.Errorf("%w：%s", ErrNotTradingDay, date)
	}
	return index, nil
}

// TradingDayAt 是 TradingDayIndex 的反向：返回序号 index 的交易日；超出日历范围返回 ErrOutOfCoverage。
func (c TradingCalendar) TradingDayAt(index int) (CivilDate, error) {
	if c.data == nil {
		return CivilDate{}, fmt.Errorf("%w：日历未加载", ErrOutOfCoverage)
	}
	if index < 0 || index >= len(c.data.tradingDays) {
		return CivilDate{}, fmt.Errorf("%w：交易日序号 %d 不在 0 至 %d 之内", ErrOutOfCoverage, index, len(c.data.tradingDays)-1)
	}
	return c.data.tradingDays[index], nil
}

func (c TradingCalendar) checkCovered(date CivilDate) error {
	if c.data == nil {
		return fmt.Errorf("%w：日历未加载", ErrOutOfCoverage)
	}
	if err := date.Validate(); err != nil {
		return err
	}
	if date.Before(c.FirstDate()) || date.After(c.LastDate()) {
		return fmt.Errorf("%w：%s 不在 %s 至 %s 之内", ErrOutOfCoverage, date, c.FirstDate(), c.LastDate())
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
		return TradingCalendar{}, fmt.Errorf("%w：清单的 calendar_id %q 与数据的 calendar_id %q 不一致", ErrInvalidManifest, manifest.CalendarID, data.calendarID)
	}
	if manifest.ValidFrom != data.dates[0].String() || manifest.ValidThrough != data.dates[len(data.dates)-1].String() {
		return TradingCalendar{}, fmt.Errorf("%w：清单的覆盖范围与数据的首末日期不一致", ErrInvalidManifest)
	}
	sum := sha256.Sum256(dataRaw)
	wantHash := "sha256:" + hex.EncodeToString(sum[:])
	if manifest.SHA256 != wantHash {
		return TradingCalendar{}, fmt.Errorf("%w：清单为 %q，数据为 %q", ErrCalendarChecksum, manifest.SHA256, wantHash)
	}
	return TradingCalendar{data: &calendarData{
		calendarID:      data.calendarID,
		tradingDays:     append([]CivilDate(nil), data.dates...),
		tradingDaySet:   data.set,
		earlyCloses:     data.earlyCloses,
		earlyCloseDates: data.earlyCloseDates,
	}}, nil
}

type parsedCalendarData struct {
	calendarID      string
	dates           []CivilDate
	set             map[CivilDate]struct{}
	earlyCloses     map[CivilDate]time.Duration
	earlyCloseDates []CivilDate
}

func parseCalendarData(raw []byte) (parsedCalendarData, error) {
	var file calendarFile
	if err := decodeJSON(raw, &file); err != nil {
		return parsedCalendarData{}, fmt.Errorf("%w：解析数据失败：%v", ErrInvalidCalendarData, err)
	}
	if strings.TrimSpace(file.CalendarID) == "" {
		return parsedCalendarData{}, fmt.Errorf("%w：缺少 calendar_id", ErrInvalidCalendarData)
	}
	if len(file.TradingDays) == 0 {
		return parsedCalendarData{}, fmt.Errorf("%w：trading_days 为空", ErrInvalidCalendarData)
	}
	dates := make([]CivilDate, 0, len(file.TradingDays))
	set := make(map[CivilDate]struct{}, len(file.TradingDays))
	for index, text := range file.TradingDays {
		date, err := ParseCivilDate(text)
		if err != nil {
			return parsedCalendarData{}, fmt.Errorf("%w：trading_days[%d] %q：%v", ErrInvalidCalendarData, index, text, err)
		}
		if _, exists := set[date]; exists {
			return parsedCalendarData{}, fmt.Errorf("%w：交易日 %q 重复", ErrInvalidCalendarData, text)
		}
		if len(dates) > 0 && !dates[len(dates)-1].Before(date) {
			return parsedCalendarData{}, fmt.Errorf("%w：trading_days[%d] %q 没有严格晚于 %q", ErrInvalidCalendarData, index, text, dates[len(dates)-1])
		}
		dates = append(dates, date)
		set[date] = struct{}{}
	}
	earlyCloses := make(map[CivilDate]time.Duration, len(file.EarlyCloses))
	earlyCloseDates := make([]CivilDate, 0, len(file.EarlyCloses))
	for text, clock := range file.EarlyCloses {
		date, err := ParseCivilDate(text)
		if err != nil {
			return parsedCalendarData{}, fmt.Errorf("%w：early_closes 的日期 %q：%v", ErrInvalidCalendarData, text, err)
		}
		if _, trading := set[date]; !trading {
			return parsedCalendarData{}, fmt.Errorf("%w：提前收市日 %q 不是交易日", ErrInvalidCalendarData, text)
		}
		parsed, err := time.Parse("15:04", clock)
		if err != nil || parsed.Hour() == 0 && parsed.Minute() == 0 {
			return parsedCalendarData{}, fmt.Errorf("%w：提前收市日 %q 的收市时刻 %q 必须写成 HH:MM", ErrInvalidCalendarData, text, clock)
		}
		earlyCloses[date] = time.Duration(parsed.Hour())*time.Hour + time.Duration(parsed.Minute())*time.Minute
		earlyCloseDates = append(earlyCloseDates, date)
	}
	sort.Slice(earlyCloseDates, func(i, j int) bool { return earlyCloseDates[i].Before(earlyCloseDates[j]) })
	return parsedCalendarData{calendarID: file.CalendarID, dates: dates, set: set, earlyCloses: earlyCloses, earlyCloseDates: earlyCloseDates}, nil
}

func parseCalendarManifest(raw []byte) (calendarManifest, error) {
	var manifest calendarManifest
	if err := decodeJSON(raw, &manifest); err != nil {
		return calendarManifest{}, fmt.Errorf("%w：解析清单失败：%v", ErrInvalidManifest, err)
	}
	if strings.TrimSpace(manifest.CalendarID) == "" || strings.TrimSpace(manifest.Source) == "" {
		return calendarManifest{}, fmt.Errorf("%w：缺少 calendar_id 或 source", ErrInvalidManifest)
	}
	if manifest.Version <= 0 {
		return calendarManifest{}, fmt.Errorf("%w：version 必须为正数", ErrInvalidManifest)
	}
	from, err := ParseCivilDate(manifest.ValidFrom)
	if err != nil {
		return calendarManifest{}, fmt.Errorf("%w：valid_from：%v", ErrInvalidManifest, err)
	}
	through, err := ParseCivilDate(manifest.ValidThrough)
	if err != nil {
		return calendarManifest{}, fmt.Errorf("%w：valid_through：%v", ErrInvalidManifest, err)
	}
	if from.After(through) {
		return calendarManifest{}, fmt.Errorf("%w：valid_from 晚于 valid_through", ErrInvalidManifest)
	}
	if !isSHA256(manifest.SHA256) {
		return calendarManifest{}, fmt.Errorf("%w：sha256 必须写成 sha256:<64 位小写十六进制>", ErrInvalidManifest)
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
			return errors.New("包含多个 JSON 值")
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
