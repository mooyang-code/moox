package stockcn

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/packages/cnstock"
)

var shanghaiLocation = mustLoadLocation("Asia/Shanghai")

func SubjectID(code string) (string, error) {
	subjectID, err := cnstock.InstrumentID(code)
	if err != nil {
		return "", fmt.Errorf("%w: %v", marketdata.ErrUnsupportedSymbol, err)
	}
	return subjectID, nil
}

func ProviderSymbol(subjectID string) (string, error) {
	symbol, err := cnstock.TencentSymbol(subjectID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", marketdata.ErrUnsupportedSymbol, err)
	}
	return symbol, nil
}

func DecodeProviderSymbol(symbol string) (string, error) {
	subjectID, err := cnstock.FromTencentSymbol(symbol)
	if err != nil {
		return "", fmt.Errorf("%w: %v", marketdata.ErrUnsupportedSymbol, err)
	}
	return subjectID, nil
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
	code, exchange, err := cnstock.SplitInstrumentID(subjectID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", marketdata.ErrUnsupportedSymbol, err)
	}
	if exchange == cnstock.ExchangeShanghai {
		return "1." + code, nil
	}
	return "0." + code, nil
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

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
