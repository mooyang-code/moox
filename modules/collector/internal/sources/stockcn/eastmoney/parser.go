package eastmoney

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
)

type response struct {
	RC   int `json:"rc"`
	RT   int `json:"rt"`
	Data *struct {
		Klines []string `json:"klines"`
	} `json:"data"`
}

func parseKlines(payload response, request marketdata.KlineRequest, now time.Time) ([]marketdata.NormalizedKline, error) {
	if payload.RC != 0 {
		return nil, fmt.Errorf("eastmoney: upstream response code %d", payload.RC)
	}
	if payload.Data == nil {
		return []marketdata.NormalizedKline{}, nil
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, fmt.Errorf("eastmoney: load Shanghai timezone: %w", err)
	}
	result := make([]marketdata.NormalizedKline, 0, len(payload.Data.Klines))
	seen := make(map[string]struct{}, len(payload.Data.Klines))
	for index, raw := range payload.Data.Klines {
		fields := strings.Split(raw, ",")
		if len(fields) < 7 {
			return nil, fmt.Errorf("eastmoney: kline %d has %d fields, want at least 7", index, len(fields))
		}
		start, err := parseTime(fields[0], location)
		if err != nil {
			return nil, fmt.Errorf("eastmoney: kline %d time: %w", index, err)
		}
		values := make([]common.Decimal, 5)
		for valueIndex, fieldIndex := range []int{1, 3, 4, 2, 5} {
			if strings.TrimSpace(fields[fieldIndex]) == "" {
				return nil, fmt.Errorf("eastmoney: kline %d field %d is empty", index, fieldIndex)
			}
			values[valueIndex] = common.NewDecimal(strings.TrimSpace(fields[fieldIndex]))
		}
		if _, exists := seen[start.UTC().Format(time.RFC3339Nano)]; exists {
			return nil, fmt.Errorf("eastmoney: duplicate kline timestamp %s", start.Format(time.RFC3339Nano))
		}
		seen[start.UTC().Format(time.RFC3339Nano)] = struct{}{}
		amount := marketdata.OptionalDecimal{Valid: true, Null: true}
		if strings.TrimSpace(fields[6]) != "" {
			amount = marketdata.OptionalDecimal{Value: common.NewDecimal(strings.TrimSpace(fields[6])), Valid: true}
		}
		result = append(result, marketdata.NormalizedKline{
			SubjectID: request.SubjectID, ProviderID: "eastmoney", SourceID: "stock_cn_http", ProviderSymbol: request.ProviderSymbol,
			Frequency: request.Frequency, BarStart: start.UTC(), BarEnd: barEnd(start, request.Frequency),
			Open: values[0], High: values[1], Low: values[2], Close: values[3], Volume: values[4], Amount: amount,
			VolumeUnit: "share", AmountUnit: "cny", ProviderTime: start, FetchedAt: now.UTC(), SourceEventID: "",
		})
	}
	return result, nil
}

func parseTime(value string, location *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

func barEnd(start time.Time, frequency string) time.Time {
	switch strings.TrimSpace(frequency) {
	case "1m":
		return start.Add(time.Minute).UTC()
	case "5m":
		return start.Add(5 * time.Minute).UTC()
	case "15m":
		return start.Add(15 * time.Minute).UTC()
	case "30m":
		return start.Add(30 * time.Minute).UTC()
	case "60m", "1h":
		return start.Add(time.Hour).UTC()
	case "1w":
		return start.AddDate(0, 0, 7).UTC()
	case "1M", "1mo", "1mth":
		return addCalendarMonth(start).UTC()
	default:
		return start.AddDate(0, 0, 1).UTC()
	}
}

func addCalendarMonth(value time.Time) time.Time {
	year, month, day := value.Date()
	firstOfNext := time.Date(year, month+1, 1, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location())
	lastDay := firstOfNext.AddDate(0, 1, -1).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(firstOfNext.Year(), firstOfNext.Month(), day, value.Hour(), value.Minute(), value.Second(), value.Nanosecond(), value.Location())
}
