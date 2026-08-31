package marketdata

import (
	"fmt"
	"strings"
)

// KlineSpec is the declared request and normalization boundary for a source.
// It intentionally contains no active rate-limit or fleet-budget settings.
type KlineSpec struct {
	MarketID              string
	MarketIDs             []string
	InstrumentType        string
	InstrumentTypes       []string
	ExchangeID            string
	Frequencies           []string
	CompleteOHLCV         bool
	HasAmount             bool
	VolumeUnit            string
	AmountUnit            string
	TimestampMode         string
	SupportsRange         bool
	MaxBarsPerRequest     int
	SupportsAdjustment    bool
	RequestTimeoutSeconds int
	HistoryStart          string
}

func (s KlineSpec) Validate() error {
	if strings.TrimSpace(s.MarketID) == "" && len(s.MarketIDs) == 0 {
		return fmt.Errorf("market_id or market_ids is required")
	}
	if strings.TrimSpace(s.InstrumentType) == "" && len(s.InstrumentTypes) == 0 {
		return fmt.Errorf("instrument_type or instrument_types is required")
	}
	if err := validateSupportValues("market_ids", s.MarketIDs); err != nil {
		return err
	}
	if err := validateSupportValues("instrument_types", s.InstrumentTypes); err != nil {
		return err
	}
	if len(s.Frequencies) == 0 {
		return fmt.Errorf("at least one frequency is required")
	}
	for _, frequency := range s.Frequencies {
		if strings.TrimSpace(frequency) == "" {
			return fmt.Errorf("frequencies must not contain empty values")
		}
	}
	if s.MaxBarsPerRequest <= 0 {
		return fmt.Errorf("max_bars_per_request must be positive")
	}
	if s.RequestTimeoutSeconds <= 0 {
		return fmt.Errorf("request_timeout_seconds must be positive")
	}
	if s.CompleteOHLCV && s.VolumeUnit == "" {
		return fmt.Errorf("volume_unit is required for complete OHLCV sources")
	}
	return nil
}

func (s KlineSpec) SupportsFrequency(frequency string) bool {
	for _, candidate := range s.Frequencies {
		if candidate == frequency {
			return true
		}
	}
	return false
}

func (s KlineSpec) SupportsMarketInstrument(marketID, instrumentType string) bool {
	marketMatch := strings.TrimSpace(s.MarketID) == strings.TrimSpace(marketID)
	if len(s.MarketIDs) > 0 {
		marketMatch = contains(s.MarketIDs, marketID)
	}
	instrumentMatch := strings.TrimSpace(s.InstrumentType) == strings.TrimSpace(instrumentType)
	if len(s.InstrumentTypes) > 0 {
		instrumentMatch = contains(s.InstrumentTypes, instrumentType)
	}
	return marketMatch && instrumentMatch
}

func contains(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

// InstrumentSpec describes a source's symbol snapshot contract.
type InstrumentSpec struct {
	MarketID        string
	MarketIDs       []string
	InstrumentType  string
	InstrumentTypes []string
	SupportsFull    bool
	SupportsPaging  bool
	HasStatus       bool
}

func (s InstrumentSpec) Validate() error {
	if strings.TrimSpace(s.MarketID) == "" && len(s.MarketIDs) == 0 {
		return fmt.Errorf("market_id or market_ids is required")
	}
	if strings.TrimSpace(s.InstrumentType) == "" && len(s.InstrumentTypes) == 0 {
		return fmt.Errorf("instrument_type or instrument_types is required")
	}
	if err := validateSupportValues("market_ids", s.MarketIDs); err != nil {
		return err
	}
	if err := validateSupportValues("instrument_types", s.InstrumentTypes); err != nil {
		return err
	}
	return nil
}

func validateSupportValues(name string, values []string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not contain empty values", name)
		}
	}
	return nil
}

func (s InstrumentSpec) SupportsMarketInstrument(marketID, instrumentType string) bool {
	marketMatch := strings.TrimSpace(s.MarketID) == strings.TrimSpace(marketID)
	if len(s.MarketIDs) > 0 {
		marketMatch = contains(s.MarketIDs, marketID)
	}
	instrumentMatch := strings.TrimSpace(s.InstrumentType) == strings.TrimSpace(instrumentType)
	if len(s.InstrumentTypes) > 0 {
		instrumentMatch = contains(s.InstrumentTypes, instrumentType)
	}
	return marketMatch && instrumentMatch
}
