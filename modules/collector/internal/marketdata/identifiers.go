package marketdata

import (
	"fmt"
	"strings"
)

type MarketID string
type ProviderID string
type ExchangeID string

type ProductType string

const (
	ProductEquity ProductType = "equity"
	ProductETF    ProductType = "etf"
	ProductIndex  ProductType = "index"
	ProductSpot   ProductType = "spot"
	ProductSwap   ProductType = "swap"
)

type InstrumentType string

const (
	InstrumentEquity InstrumentType = "equity"
	InstrumentETF    InstrumentType = "etf"
	InstrumentIndex  InstrumentType = "index"
	InstrumentSpot   InstrumentType = "spot"
	InstrumentSwap   InstrumentType = "swap"
)

type Frequency string

const (
	FrequencyMinute Frequency = "1m"
	Frequency5Min   Frequency = "5m"
	Frequency15Min  Frequency = "15m"
	Frequency30Min  Frequency = "30m"
	FrequencyHour   Frequency = "1h"
	Frequency4Hour  Frequency = "4h"
	FrequencyDay    Frequency = "1d"
	FrequencyWeek   Frequency = "1w"
)

func ParseFrequency(input string) (Frequency, error) {
	normalized := strings.ToLower(strings.TrimSpace(input))
	if normalized == "60m" {
		normalized = string(FrequencyHour)
	}
	frequency := Frequency(normalized)
	switch frequency {
	case FrequencyMinute, Frequency5Min, Frequency15Min, Frequency30Min,
		FrequencyHour, Frequency4Hour, FrequencyDay, FrequencyWeek:
		return frequency, nil
	default:
		return "", fmt.Errorf("unsupported frequency %q", input)
	}
}

func (f Frequency) DurationMinutes() int {
	switch f {
	case FrequencyMinute:
		return 1
	case Frequency5Min:
		return 5
	case Frequency15Min:
		return 15
	case Frequency30Min:
		return 30
	case FrequencyHour:
		return 60
	case Frequency4Hour:
		return 240
	case FrequencyDay:
		return 1440
	case FrequencyWeek:
		return 10080
	default:
		return 0
	}
}
