// Package frequency defines the canonical time-series frequencies shared by
// Storage, Collector, Factor and the CLI.
//
// 规范频率由语法定义，而不是一张枚举：日内周期写成 "<N>m" 或 "<N>h"，且必须能整除一天，
// 这样桶边界每天都从同一时刻重新开始；"30s"、"1d"、"1w"、"1mo" 是固定写法。
// 同一个周期只有一种规范写法（60m → 1h、1440m → 1d），别名只在输入边界归一，
// Storage 只接受规范值，避免 "60m" 与 "1h" 在存储里变成两条序列。
// 交易所的 API 编码留在各数据源适配器里。
package frequency

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Frequency is a canonical bar length such as "1m", "45m" or "1mo".
type Frequency string

const (
	Second30 Frequency = "30s"
	Minute1  Frequency = "1m"
	Minute5  Frequency = "5m"
	Minute15 Frequency = "15m"
	Minute30 Frequency = "30m"
	Hour1    Frequency = "1h"
	Hour4    Frequency = "4h"
	Day1     Frequency = "1d"
	Week1    Frequency = "1w"
	Month1   Frequency = "1mo"
)

const minutesPerDay = 24 * 60

// standard 是下拉候选、保留期表格与错误提示使用的标准档；它不是“全部合法值”。
var standard = []Frequency{Second30, Minute1, Minute5, Minute15, Minute30, Hour1, Hour4, Day1, Week1, Month1}

// aliases maps accepted spellings to canonical values. Matching is exact:
// "1M" is a month (exchange convention) while "1m" is a minute.
var aliases = map[string]Frequency{
	"30S": Second30,
	"1D":  Day1,
	"1W":  Week1,
	"1M":  Month1, "1MO": Month1, "1Mo": Month1,
}

// Standard returns the standard frequencies from the shortest bar to the longest.
// Other grammar-valid values (for example "45m" or "2h") are accepted too.
func Standard() []Frequency {
	return append([]Frequency(nil), standard...)
}

// StandardStrings returns the standard frequencies as strings.
func StandardStrings() []string {
	values := make([]string, len(standard))
	for i, frequency := range standard {
		values[i] = string(frequency)
	}
	return values
}

// Parse returns the canonical frequency for raw, accepting aliases such as
// "60m" (1h), "1H" and "1440m" (1d).
func Parse(raw string) (Frequency, error) {
	value := strings.TrimSpace(raw)
	if canonical, ok := aliases[value]; ok {
		return canonical, nil
	}
	switch Frequency(value) {
	case Second30, Day1, Week1, Month1:
		return Frequency(value), nil
	}
	if canonical, ok := parseIntraday(value); ok {
		return canonical, nil
	}
	return "", fmt.Errorf("unsupported frequency %q; use <N>m or <N>h that divides one day (for example %s)", raw, strings.Join(StandardStrings(), ", "))
}

// parseIntraday 解析 "<N>m" / "<N>h" / "<N>H"：N 不带前导零，总分钟数能整除一天，返回规范写法。
func parseIntraday(value string) (Frequency, bool) {
	if len(value) < 2 {
		return "", false
	}
	unit := value[len(value)-1]
	digits := value[:len(value)-1]
	if digits[0] < '1' || digits[0] > '9' {
		return "", false
	}
	for _, char := range digits {
		if char < '0' || char > '9' {
			return "", false
		}
	}
	// 位数过多必然超出一天，也避免溢出。
	if len(digits) > 5 {
		return "", false
	}
	number, err := strconv.Atoi(digits)
	if err != nil {
		return "", false
	}
	var minutes int
	switch unit {
	case 'm':
		minutes = number
	case 'h', 'H':
		minutes = number * 60
	default:
		return "", false
	}
	if minutes <= 0 || minutes > minutesPerDay || minutesPerDay%minutes != 0 {
		return "", false
	}
	return fromMinutes(minutes), true
}

// fromMinutes returns the single canonical spelling of an intraday length.
func fromMinutes(minutes int) Frequency {
	switch {
	case minutes == minutesPerDay:
		return Day1
	case minutes%60 == 0:
		return Frequency(strconv.Itoa(minutes/60) + "h")
	default:
		return Frequency(strconv.Itoa(minutes) + "m")
	}
}

// Normalize returns the canonical string for raw, accepting known aliases.
func Normalize(raw string) (string, error) {
	parsed, err := Parse(raw)
	return string(parsed), err
}

// IsCanonical reports whether raw is a valid frequency already in its single canonical spelling.
func IsCanonical(raw string) bool {
	parsed, err := Parse(raw)
	return err == nil && string(parsed) == raw
}

// Duration returns the fixed bar length. A month has no fixed length, so
// Month1 returns 0; use BarEnd for calendar arithmetic.
func (f Frequency) Duration() time.Duration {
	switch f {
	case Second30:
		return 30 * time.Second
	case Week1:
		return 7 * 24 * time.Hour
	case Month1:
		return 0
	}
	if canonical, ok := parseIntraday(string(f)); ok && canonical == f {
		unit := f[len(f)-1]
		number, _ := strconv.Atoi(string(f[:len(f)-1]))
		if unit == 'h' {
			return time.Duration(number) * time.Hour
		}
		return time.Duration(number) * time.Minute
	}
	if f == Day1 {
		return 24 * time.Hour
	}
	return 0
}

// NominalDuration returns the bar length for tolerances such as freshness
// windows, counting a month as 30 days. Never use it for bar boundaries.
func (f Frequency) NominalDuration() time.Duration {
	if f == Month1 {
		return 30 * 24 * time.Hour
	}
	return f.Duration()
}

// BarEnd returns the exclusive end of the bar that starts at start.
func (f Frequency) BarEnd(start time.Time) time.Time {
	if f == Month1 {
		return start.AddDate(0, 1, 0)
	}
	return start.Add(f.Duration())
}

// IsIntraday reports whether f is a fixed bar shorter than one day.
func (f Frequency) IsIntraday() bool {
	duration := f.Duration()
	return duration > 0 && duration < 24*time.Hour
}

// String implements fmt.Stringer.
func (f Frequency) String() string { return string(f) }
