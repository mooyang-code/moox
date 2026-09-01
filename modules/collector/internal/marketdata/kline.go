package marketdata

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
)

// KlineRequest is shared by HTTP and TCP source adapters.
type KlineRequest struct {
	MarketID       string
	InstrumentType string
	ExchangeID     string
	SubjectID      string
	ProviderSymbol string
	Frequency      string
	Limit          int
	StartTime      time.Time
	EndTime        time.Time
}

func (r KlineRequest) Validate() error {
	if strings.TrimSpace(r.MarketID) == "" || strings.TrimSpace(r.InstrumentType) == "" {
		return fmt.Errorf("market_id and instrument_type are required")
	}
	if strings.TrimSpace(r.SubjectID) == "" {
		return fmt.Errorf("subject_id is required")
	}
	if strings.TrimSpace(r.ProviderSymbol) == "" {
		return fmt.Errorf("provider_symbol is required")
	}
	if strings.TrimSpace(r.Frequency) == "" {
		return fmt.Errorf("frequency is required")
	}
	if r.Limit < 0 {
		return fmt.Errorf("limit cannot be negative")
	}
	if !r.StartTime.IsZero() && !r.EndTime.IsZero() && r.EndTime.Before(r.StartTime) {
		return fmt.Errorf("end_time cannot be before start_time")
	}
	return nil
}

// OptionalDecimal preserves the difference between an omitted value and an
// explicit null. This matters because Storage upserts fields independently.
type OptionalDecimal struct {
	Value common.Decimal
	Valid bool
	Null  bool
}

// NormalizedKline is the canonical, single-source bar shape.
type NormalizedKline struct {
	SubjectID      string
	ProviderID     string
	SourceID       string
	ProviderSymbol string
	Frequency      string
	BarStart       time.Time
	BarEnd         time.Time
	Open           common.Decimal
	High           common.Decimal
	Low            common.Decimal
	Close          common.Decimal
	Volume         common.Decimal
	Amount         OptionalDecimal
	VolumeUnit     string
	AmountUnit     string
	ProviderTime   time.Time
	FetchedAt      time.Time
	SourceEventID  string
}

func (k NormalizedKline) Validate() error {
	if err := k.ValidateOHLCV(); err != nil {
		return err
	}
	if k.Amount.Valid && !k.Amount.Null {
		if err := validateDecimal("amount", k.Amount.Value, true); err != nil {
			return err
		}
	}
	return nil
}

// ValidateOHLCV validates the fields that are expected to be present even on
// an unsettled provider bar. Amount can be unavailable until the bar closes,
// so callers may use this method before applying their settle-delay policy.
func (k NormalizedKline) ValidateOHLCV() error {
	if strings.TrimSpace(k.SubjectID) == "" {
		return fmt.Errorf("subject_id is required")
	}
	if strings.TrimSpace(k.ProviderID) == "" || strings.TrimSpace(k.SourceID) == "" {
		return fmt.Errorf("provider_id and source_id are required")
	}
	if strings.TrimSpace(k.ProviderSymbol) == "" {
		return fmt.Errorf("provider_symbol is required")
	}
	if strings.TrimSpace(k.Frequency) == "" {
		return fmt.Errorf("frequency is required")
	}
	if k.BarStart.IsZero() || k.BarEnd.IsZero() || !k.BarEnd.After(k.BarStart) {
		return fmt.Errorf("bar_start and bar_end must define a positive interval")
	}
	if k.VolumeUnit == "" {
		return fmt.Errorf("volume_unit is required")
	}
	for field, value := range map[string]common.Decimal{
		"open":   k.Open,
		"high":   k.High,
		"low":    k.Low,
		"close":  k.Close,
		"volume": k.Volume,
	} {
		nonNegative := field == "volume"
		if err := validateDecimal(field, value, nonNegative); err != nil {
			return err
		}
	}
	if high, err := k.High.Float64(); err != nil {
		return fmt.Errorf("high: %w", err)
	} else if low, lowErr := k.Low.Float64(); lowErr != nil {
		return fmt.Errorf("low: %w", lowErr)
	} else if high < low {
		return fmt.Errorf("high cannot be below low")
	}
	return nil
}

func validateDecimal(name string, value common.Decimal, nonNegative bool) error {
	number, err := value.Float64()
	if err != nil {
		return fmt.Errorf("%s: invalid decimal: %w", name, err)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return fmt.Errorf("%s: NaN or Inf is not allowed", name)
	}
	if nonNegative && number < 0 {
		return fmt.Errorf("%s: negative value is not allowed", name)
	}
	return nil
}
