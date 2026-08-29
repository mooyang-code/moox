package marketdata

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type NormalizedKline struct {
	SubjectID         string
	ProviderID        string
	ProviderSymbol    string
	Frequency         string
	BarStart          time.Time
	BarEnd            time.Time
	Open              float64
	High              float64
	Low               float64
	Close             float64
	VolumeShares      float64
	AmountCNY         float64
	TradeCount        int64
	ProviderTimestamp time.Time
	FetchedAt         time.Time
	RequestID         string
}

func ValidateNormalizedKline(kline NormalizedKline) error {
	if strings.TrimSpace(kline.SubjectID) == "" {
		return fmt.Errorf("subject_id is required")
	}
	if strings.TrimSpace(kline.ProviderID) == "" {
		return fmt.Errorf("provider_id is required")
	}
	if strings.TrimSpace(kline.ProviderSymbol) == "" {
		return fmt.Errorf("provider_symbol is required")
	}
	frequency, err := ParseFrequency(kline.Frequency)
	if err != nil {
		return err
	}
	if frequency != FrequencyMinute {
		return fmt.Errorf("%w: normalized kline only supports 1m", ErrUnsupportedFrequency)
	}
	if !isUTCMinute(kline.BarStart) {
		return fmt.Errorf("bar_start must be a UTC minute bucket")
	}
	if !kline.BarEnd.Equal(kline.BarStart.Add(time.Minute)) {
		return fmt.Errorf("bar_end must equal bar_start + 1m")
	}
	for _, field := range []struct {
		name  string
		value float64
		min   float64
	}{
		{name: "open", value: kline.Open, min: math.SmallestNonzeroFloat64},
		{name: "high", value: kline.High, min: math.SmallestNonzeroFloat64},
		{name: "low", value: kline.Low, min: math.SmallestNonzeroFloat64},
		{name: "close", value: kline.Close, min: math.SmallestNonzeroFloat64},
		{name: "volume_shares", value: kline.VolumeShares, min: 0},
		{name: "amount_cny", value: kline.AmountCNY, min: 0},
	} {
		if !isFinite(field.value) {
			return fmt.Errorf("%s must be finite", field.name)
		}
		if field.value < field.min || (field.min > 0 && field.value == 0) {
			return fmt.Errorf("%s must be positive", field.name)
		}
	}
	if kline.TradeCount < 0 {
		return fmt.Errorf("trade_count must be non-negative")
	}
	if kline.High < maxFloat(kline.Open, kline.Close, kline.Low) {
		return fmt.Errorf("high must be >= open/close/low")
	}
	if kline.Low > minFloat(kline.Open, kline.Close, kline.High) {
		return fmt.Errorf("low must be <= open/close/high")
	}
	if !isUTCTime(kline.ProviderTimestamp) || !isUTCTime(kline.FetchedAt) {
		return fmt.Errorf("provider_timestamp and fetched_at must be UTC")
	}
	if strings.TrimSpace(kline.RequestID) == "" {
		return fmt.Errorf("request_id is required")
	}
	return nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func isUTCMinute(value time.Time) bool {
	return isUTCTime(value) && value.Second() == 0 && value.Nanosecond() == 0
}

func isUTCTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func maxFloat(values ...float64) float64 {
	maxValue := values[0]
	for _, value := range values[1:] {
		if value > maxValue {
			maxValue = value
		}
	}
	return maxValue
}

func minFloat(values ...float64) float64 {
	minValue := values[0]
	for _, value := range values[1:] {
		if value < minValue {
			minValue = value
		}
	}
	return minValue
}
