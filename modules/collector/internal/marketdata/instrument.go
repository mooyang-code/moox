package marketdata

import (
	"fmt"
	"strings"
)

type InstrumentRequest struct {
	MarketID       string
	InstrumentType string
	ExchangeID     string
	ProviderSymbol string
	Page           int
	PageSize       int
}

func (r InstrumentRequest) Validate() error {
	if strings.TrimSpace(r.MarketID) == "" || strings.TrimSpace(r.InstrumentType) == "" {
		return fmt.Errorf("market_id and instrument_type are required")
	}
	if r.Page < 0 || r.PageSize < 0 {
		return fmt.Errorf("page and page_size cannot be negative")
	}
	return nil
}

type Instrument struct {
	SubjectID      string
	ProviderSymbol string
	Name           string
	ExchangeID     string
	Status         string
	InstrumentType string
}

type InstrumentSnapshot struct {
	MarketID       string
	InstrumentType string
	Items          []Instrument
	Version        string
}

func (s InstrumentSnapshot) Validate() error {
	if strings.TrimSpace(s.MarketID) == "" || strings.TrimSpace(s.InstrumentType) == "" {
		return fmt.Errorf("market_id and instrument_type are required")
	}
	if strings.TrimSpace(s.Version) == "" {
		return fmt.Errorf("snapshot version is required")
	}
	seen := make(map[string]struct{}, len(s.Items))
	for _, item := range s.Items {
		if item.SubjectID == "" || item.ProviderSymbol == "" {
			return fmt.Errorf("instrument subject_id and provider_symbol are required")
		}
		if _, exists := seen[item.SubjectID]; exists {
			return fmt.Errorf("duplicate instrument subject_id %q", item.SubjectID)
		}
		seen[item.SubjectID] = struct{}{}
	}
	return nil
}
