package stockcn

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

var shanghaiLocation = mustLoadLocation("Asia/Shanghai")

func SubjectID(code string) (string, error) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return "", fmt.Errorf("%w: China security code must contain six digits", marketdata.ErrUnsupportedSymbol)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("%w: invalid security code", marketdata.ErrUnsupportedSymbol)
		}
	}
	switch code[0] {
	case '6':
		return code + ".XSHG", nil
	case '0', '2', '3':
		return code + ".XSHE", nil
	case '4', '8', '9':
		return code + ".XBSE", nil
	default:
		return "", fmt.Errorf("%w: unsupported exchange for code %s", marketdata.ErrUnsupportedSymbol, code)
	}
}

func ProviderSymbol(subjectID string) (string, error) {
	code, exchange, err := splitSubjectID(subjectID)
	if err != nil {
		return "", err
	}
	switch exchange {
	case "XSHG":
		return "sh" + code, nil
	case "XSHE":
		return "sz" + code, nil
	case "XBSE":
		return "bj" + code, nil
	default:
		return "", fmt.Errorf("%w: unknown exchange %s", marketdata.ErrUnsupportedSymbol, exchange)
	}
}

func DecodeProviderSymbol(symbol string) (string, error) {
	symbol = strings.ToLower(strings.TrimSpace(symbol))
	if len(symbol) != 8 {
		return "", fmt.Errorf("%w: invalid provider symbol %q", marketdata.ErrUnsupportedSymbol, symbol)
	}
	code := symbol[2:]
	switch symbol[:2] {
	case "sh":
		return SubjectIDWithExchange(code, "XSHG")
	case "sz":
		return SubjectIDWithExchange(code, "XSHE")
	case "bj":
		return SubjectIDWithExchange(code, "XBSE")
	default:
		return "", fmt.Errorf("%w: invalid provider symbol prefix %q", marketdata.ErrUnsupportedSymbol, symbol[:2])
	}
}

func SubjectIDWithExchange(code, exchange string) (string, error) {
	subjectID, err := SubjectID(code)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(subjectID, "."+exchange) {
		return "", fmt.Errorf("%w: code %s does not belong to exchange %s", marketdata.ErrUnsupportedSymbol, code, exchange)
	}
	return subjectID, nil
}

func EastMoneySecID(subjectID string) (string, error) {
	code, exchange, err := splitSubjectID(subjectID)
	if err != nil {
		return "", err
	}
	switch exchange {
	case "XSHG":
		return "1." + code, nil
	case "XSHE", "XBSE":
		return "0." + code, nil
	default:
		return "", fmt.Errorf("%w: unknown exchange %s", marketdata.ErrUnsupportedSymbol, exchange)
	}
}

func ParseCNMinute(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	layouts := []string{"2006-01-02 15:04", "2006-01-02 15:04:05", "200601021504"}
	for _, layout := range layouts {
		if ts, err := time.ParseInLocation(layout, value, shanghaiLocation); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: invalid minute timestamp %q", marketdata.ErrProtocol, value)
}

func ParseFloat(value string) (float64, error) {
	number, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(value, ",", "")), 64)
	if err != nil {
		return 0, fmt.Errorf("%w: parse float %q: %v", marketdata.ErrProtocol, value, err)
	}
	return number, nil
}

// NormalizeIntradayKline 把一根日内 K 线归一化：周期取请求频率，mode 说明上游时间标签是开盘时刻还是收盘时刻。
func NormalizeIntradayKline(subjectID, providerID, providerSymbol string, frequency marketdata.Frequency, mode marketdata.TimestampMode, rawTime string, open, high, low, close, volume, amount float64, volumeMultiplier float64, fetchedAt time.Time, requestID string) (marketdata.NormalizedKline, error) {
	period := frequency.Duration()
	if period <= 0 || period >= 24*time.Hour {
		return marketdata.NormalizedKline{}, fmt.Errorf("%w: %q 不是日内频率", marketdata.ErrUnsupportedFrequency, frequency)
	}
	barTime, err := ParseCNMinute(rawTime)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	barStart := barTime
	barEnd := barTime.Add(period)
	if mode == marketdata.TimestampModeClose {
		barEnd = barTime
		barStart = barEnd.Add(-period)
	}
	row := marketdata.NormalizedKline{
		SubjectID:         subjectID,
		ProviderID:        providerID,
		ProviderSymbol:    providerSymbol,
		Frequency:         string(frequency),
		BarStart:          barStart.UTC(),
		BarEnd:            barEnd.UTC(),
		Open:              open,
		High:              high,
		Low:               low,
		Close:             close,
		Volume:            volume * volumeMultiplier,
		Amount:            amount,
		ProviderTimestamp: barEnd.UTC(),
		FetchedAt:         fetchedAt.UTC(),
		RequestID:         requestID,
	}
	if err := marketdata.ValidateNormalizedKline(row); err != nil {
		return marketdata.NormalizedKline{}, err
	}
	return row, nil
}

// NormalizeDailyKline 把一根日线归一化：rawDate 是上游的交易日期标签（2006-01-02），键是交易日期的 UTC 零点
// （与 barclock 的 1d 键一致），bar_end 是键加 24 小时；上游时间戳记当日 15:00 收盘。
func NormalizeDailyKline(subjectID, providerID, providerSymbol, rawDate string, open, high, low, close, volume, amount float64, volumeMultiplier float64, fetchedAt time.Time, requestID string) (marketdata.NormalizedKline, error) {
	barStart, err := time.Parse(time.DateOnly, strings.TrimSpace(rawDate))
	if err != nil {
		return marketdata.NormalizedKline{}, fmt.Errorf("%w: invalid trade date %q", marketdata.ErrProtocol, rawDate)
	}
	row := marketdata.NormalizedKline{
		SubjectID:         subjectID,
		ProviderID:        providerID,
		ProviderSymbol:    providerSymbol,
		Frequency:         string(marketdata.FrequencyDay),
		BarStart:          barStart,
		BarEnd:            marketdata.FrequencyDay.BarEnd(barStart),
		Open:              open,
		High:              high,
		Low:               low,
		Close:             close,
		Volume:            volume * volumeMultiplier,
		Amount:            amount,
		ProviderTimestamp: time.Date(barStart.Year(), barStart.Month(), barStart.Day(), 15, 0, 0, 0, shanghaiLocation).UTC(),
		FetchedAt:         fetchedAt.UTC(),
		RequestID:         requestID,
	}
	if err := marketdata.ValidateNormalizedKline(row); err != nil {
		return marketdata.NormalizedKline{}, err
	}
	return row, nil
}

// LatestPageLimit 返回只给“最新一页”、没有起点游标的接口（新浪、腾讯）应请求的根数。
// 没有起始时间时取请求的 Limit；有起始时间时按交易序号数出起始时间到现在的交易桶数，多取 2 根
// （容忍未收盘那根与服务端延迟），不超过 maxBars。实时周期提交只要几根，长区间补采自然更多。
func LatestPageLimit(req marketdata.KlineRequest, now time.Time, maxBars int) int {
	if req.StartTime.IsZero() {
		return req.Limit
	}
	clock, err := markets.Clock("stockcn", req.Frequency)
	if err != nil {
		return maxBars
	}
	newest, err := clock.LatestClosed(now)
	if err != nil {
		return maxBars
	}
	before, err := clock.LatestClosed(req.StartTime)
	if err != nil {
		return maxBars
	}
	first, err := clock.Next(before)
	if err != nil {
		return maxBars
	}
	newestOrdinal, err := clock.Ordinal(newest)
	if err != nil {
		return maxBars
	}
	firstOrdinal, err := clock.Ordinal(first)
	if err != nil {
		return maxBars
	}
	return min(max(int(newestOrdinal-firstOrdinal)+3, 3), maxBars)
}

func splitSubjectID(subjectID string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(subjectID), ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("%w: invalid subject_id %q", marketdata.ErrUnsupportedSymbol, subjectID)
	}
	if len(parts[0]) != 6 {
		return "", "", fmt.Errorf("%w: invalid subject code %q", marketdata.ErrUnsupportedSymbol, parts[0])
	}
	for _, r := range parts[0] {
		if r < '0' || r > '9' {
			return "", "", fmt.Errorf("%w: invalid subject code %q", marketdata.ErrUnsupportedSymbol, parts[0])
		}
	}
	code, exchange := parts[0], strings.ToUpper(parts[1])
	switch exchange {
	case "XSHG":
		if code[0] != '6' {
			return "", "", fmt.Errorf("%w: code %q does not belong to XSHG", marketdata.ErrUnsupportedSymbol, code)
		}
	case "XSHE":
		if code[0] != '0' && code[0] != '2' && code[0] != '3' {
			return "", "", fmt.Errorf("%w: code %q does not belong to XSHE", marketdata.ErrUnsupportedSymbol, code)
		}
	case "XBSE":
		if code[0] != '4' && code[0] != '8' && code[0] != '9' {
			return "", "", fmt.Errorf("%w: code %q does not belong to XBSE", marketdata.ErrUnsupportedSymbol, code)
		}
	default:
		return "", "", fmt.Errorf("%w: unsupported exchange %q", marketdata.ErrUnsupportedSymbol, parts[1])
	}
	return code, exchange, nil
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
