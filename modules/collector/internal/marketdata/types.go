package marketdata

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

type MarketID string
type ProviderID string
type ExchangeID string
type ProductType string
type InstrumentType string

const (
	ProductEquity ProductType = "equity"
	ProductETF    ProductType = "etf"
	ProductIndex  ProductType = "index"
	ProductSpot   ProductType = "spot"
	ProductSwap   ProductType = "swap"
)

const (
	InstrumentEquity InstrumentType = "equity"
	InstrumentETF    InstrumentType = "etf"
	InstrumentIndex  InstrumentType = "index"
	InstrumentSpot   InstrumentType = "spot"
	InstrumentSwap   InstrumentType = "swap"
)

type TimestampMode string

const (
	TimestampModeOpen  TimestampMode = "open"
	TimestampModeClose TimestampMode = "close"
)

type RateLimitPolicy struct {
	RequestsPerSecond float64
	Burst             int
	MaxConcurrent     int
	Cooldown          time.Duration
	RequestTimeout    time.Duration
}

func (p RateLimitPolicy) Validate() error {
	switch {
	case math.IsNaN(p.RequestsPerSecond), math.IsInf(p.RequestsPerSecond, 0), p.RequestsPerSecond <= 0:
		return fmt.Errorf("requests_per_second must be a finite positive number")
	case p.Burst <= 0:
		return fmt.Errorf("burst must be positive")
	case p.MaxConcurrent <= 0:
		return fmt.Errorf("max_concurrent must be positive")
	case p.Cooldown <= 0:
		return fmt.Errorf("cooldown must be positive")
	case p.RequestTimeout <= 0:
		return fmt.Errorf("request_timeout must be positive")
	default:
		return nil
	}
}

type KlineSpec struct {
	Markets           []string
	Exchanges         []string
	Frequencies       []string
	CompleteOHLCV     bool
	HasAmount         bool
	MaxBarsPerRequest int
	SupportsBatch     bool
	TimestampMode     TimestampMode
	RateLimit         RateLimitPolicy
}

func (s KlineSpec) Validate() error {
	if len(s.Markets) == 0 || len(s.Exchanges) == 0 || len(s.Frequencies) == 0 {
		return fmt.Errorf("markets, exchanges and frequencies are required")
	}
	for _, freq := range s.Frequencies {
		if _, err := ParseFrequency(freq); err != nil {
			return err
		}
	}
	if s.MaxBarsPerRequest <= 0 {
		return fmt.Errorf("max_bars_per_request must be positive")
	}
	switch s.TimestampMode {
	case TimestampModeOpen, TimestampModeClose:
	default:
		return fmt.Errorf("unsupported timestamp mode %q", s.TimestampMode)
	}
	return s.RateLimit.Validate()
}

type InstrumentSpec struct {
	Markets                []string
	Exchanges              []string
	FullSnapshot           bool
	PageSize               int
	SupportsDelistedStatus bool
	RateLimit              RateLimitPolicy
}

func (s InstrumentSpec) Validate() error {
	if len(s.Markets) == 0 || len(s.Exchanges) == 0 {
		return fmt.Errorf("markets and exchanges are required")
	}
	if !s.FullSnapshot {
		return fmt.Errorf("instrument feeds must return a complete snapshot")
	}
	if s.PageSize <= 0 {
		return fmt.Errorf("page_size must be positive")
	}
	return s.RateLimit.Validate()
}

type ProviderDescriptor struct {
	ID          string
	DisplayName string
	Hosts       []string
}

var providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (d ProviderDescriptor) Validate() error {
	if !providerIDPattern.MatchString(strings.TrimSpace(d.ID)) {
		return fmt.Errorf("invalid provider id %q", d.ID)
	}
	if strings.TrimSpace(d.DisplayName) == "" {
		return fmt.Errorf("display_name is required")
	}
	if len(d.Hosts) == 0 {
		return fmt.Errorf("hosts are required")
	}
	for _, host := range d.Hosts {
		if strings.TrimSpace(host) == "" || strings.Contains(host, " ") {
			return fmt.Errorf("invalid host %q", host)
		}
	}
	return nil
}

type MarketProvider interface {
	Descriptor() ProviderDescriptor
}

type KlineRequest struct {
	MarketID       MarketID
	ExchangeID     ExchangeID
	ProductType    ProductType
	InstrumentType InstrumentType
	SubjectID      string
	ProviderSymbol string
	Frequency      string
	Limit          int
	StartTime      time.Time
	EndTime        time.Time
	Now            time.Time
	RequestID      string
}

func (r KlineRequest) FrequencyValue() (Frequency, error) {
	return ParseFrequency(r.Frequency)
}

func (r KlineRequest) Validate() error {
	if strings.TrimSpace(r.SubjectID) == "" {
		return fmt.Errorf("%w: subject_id is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.ProviderSymbol) == "" {
		return fmt.Errorf("%w: provider_symbol is required", ErrInvalidRequest)
	}
	if _, err := r.FrequencyValue(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupportedFrequency, err)
	}
	if r.Limit <= 0 {
		return fmt.Errorf("%w: limit must be positive", ErrInvalidRequest)
	}
	if strings.TrimSpace(r.RequestID) == "" {
		return fmt.Errorf("%w: request_id is required", ErrInvalidRequest)
	}
	return nil
}

type KlineFetcher interface {
	MarketProvider
	KlineSpec() KlineSpec
	FetchKlines(context.Context, KlineRequest) ([]NormalizedKline, error)
}

type InstrumentRequest struct {
	MarketID   MarketID
	ExchangeID ExchangeID
	SnapshotAt time.Time
	RequestID  string
}

type InstrumentFetcher interface {
	MarketProvider
	InstrumentSpec() InstrumentSpec
	FetchInstrumentSnapshot(context.Context, InstrumentRequest) (InstrumentSnapshot, error)
}

type Instrument struct {
	SubjectID      string
	ProviderSymbol string
	Exchange       string
	Name           string
	Status         string
}

type InstrumentSnapshot struct {
	SnapshotID     string
	SourceProvider string
	MarketID       string
	FetchedAt      time.Time
	Complete       bool
	PageCount      int
	ExchangeCounts map[string]int
	Instruments    []Instrument
}
