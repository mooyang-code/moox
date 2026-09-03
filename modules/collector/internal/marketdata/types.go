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
type SourceID string
type ExchangeID string
type ProductType string
type InstrumentType string

type SourceKey struct {
	ProviderID string
	SourceID   string
}

func (key SourceKey) String() string {
	providerID := strings.TrimSpace(key.ProviderID)
	sourceID := strings.TrimSpace(key.SourceID)
	if sourceID == "" {
		return providerID
	}
	return providerID + "/" + sourceID
}

func (key SourceKey) Validate() error {
	if !providerIDPattern.MatchString(strings.ToLower(strings.TrimSpace(key.ProviderID))) {
		return fmt.Errorf("invalid provider id %q", key.ProviderID)
	}
	if sourceID := strings.TrimSpace(key.SourceID); sourceID != "" && !providerIDPattern.MatchString(strings.ToLower(sourceID)) {
		return fmt.Errorf("invalid source id %q", key.SourceID)
	}
	return nil
}

func NewSourceKey(providerID, sourceID string) (SourceKey, error) {
	key := SourceKey{ProviderID: strings.ToLower(strings.TrimSpace(providerID)), SourceID: strings.ToLower(strings.TrimSpace(sourceID))}
	if err := key.Validate(); err != nil {
		return SourceKey{}, err
	}
	return key, nil
}

type SourceStatus string

const (
	SourceEnabled     SourceStatus = "enabled"
	SourceShadow      SourceStatus = "shadow"
	SourceCatalogOnly SourceStatus = "catalog_only"
)

type SourceSpec struct {
	Key             SourceKey
	Status          SourceStatus
	ProtocolVariant string
	Transport       string
	Host            string
	Port            int
	Markets         []MarketID
	Instruments     []InstrumentType
	Frequencies     []string
	TimestampMode   TimestampMode
	CompleteOHLCV   bool
	HasAmount       bool
}

func (spec SourceSpec) Validate() error {
	if strings.TrimSpace(spec.Key.SourceID) == "" {
		return fmt.Errorf("source_id is required")
	}
	if err := spec.Key.Validate(); err != nil {
		return err
	}
	switch spec.Status {
	case SourceEnabled, SourceShadow, SourceCatalogOnly:
	default:
		return fmt.Errorf("invalid source status %q", spec.Status)
	}
	if strings.TrimSpace(spec.ProtocolVariant) == "" || strings.TrimSpace(spec.Transport) == "" || strings.TrimSpace(spec.Host) == "" {
		return fmt.Errorf("protocol_variant, transport and host are required")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if len(spec.Markets) == 0 || len(spec.Instruments) == 0 || len(spec.Frequencies) == 0 {
		return fmt.Errorf("markets, instruments and frequencies are required")
	}
	for _, frequency := range spec.Frequencies {
		if _, err := ParseFrequency(frequency); err != nil {
			return err
		}
	}
	switch spec.TimestampMode {
	case TimestampModeOpen, TimestampModeClose:
	default:
		return fmt.Errorf("unsupported timestamp mode %q", spec.TimestampMode)
	}
	return nil
}

const (
	ProductEquity          ProductType = "equity"
	ProductETF             ProductType = "etf"
	ProductIndex           ProductType = "index"
	ProductConvertibleBond ProductType = "convertible_bond"
	ProductSpot            ProductType = "spot"
	ProductSwap            ProductType = "swap"
)

const (
	InstrumentEquity          InstrumentType = "equity"
	InstrumentETF             InstrumentType = "etf"
	InstrumentIndex           InstrumentType = "index"
	InstrumentConvertibleBond InstrumentType = "convertible_bond"
	InstrumentSpot            InstrumentType = "spot"
	InstrumentSwap            InstrumentType = "swap"
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
	History           KlineHistoryCapability
}

// SupportsRequest reports whether the static feed contract can serve the
// request without making an HTTP call. An empty request dimension is treated
// as unspecified so crypto callers that do not carry an exchange remain
// compatible with the common router.
func (s KlineSpec) SupportsRequest(req KlineRequest) bool {
	if market := strings.TrimSpace(string(req.MarketID)); market != "" && !containsFold(s.Markets, market) {
		return false
	}
	if frequency := strings.TrimSpace(req.Frequency); frequency != "" && !containsFold(s.Frequencies, frequency) {
		return false
	}
	if strings.TrimSpace(string(req.ExchangeID)) != "" && !containsFold(s.Exchanges, string(req.ExchangeID)) {
		return false
	}
	return true
}

func containsFold(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

// KlineHistoryCapability describes how a provider handles a non-zero
// StartTime. A provider without arbitrary-range support may only be used when
// the requested start is inside MaxLookback and its response proves coverage.
type KlineHistoryCapability struct {
	SupportsArbitraryRange bool
	MaxLookback            time.Duration
}

func (c KlineHistoryCapability) ValidateStart(now, start time.Time) error {
	if start.IsZero() {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if start.After(now) {
		return fmt.Errorf("%w: history start %s is after reference time %s", ErrHistoryOutOfRange, start.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	}
	if c.SupportsArbitraryRange {
		return nil
	}
	if c.MaxLookback <= 0 {
		return fmt.Errorf("%w: provider does not declare a bounded history window", ErrHistoryOutOfRange)
	}
	if start.Before(now.Add(-c.MaxLookback)) {
		return fmt.Errorf("%w: history start %s exceeds provider bounded lookback %s", ErrHistoryOutOfRange, start.UTC().Format(time.RFC3339Nano), c.MaxLookback)
	}
	return nil
}

func (c KlineHistoryCapability) ValidateCoverage(rows []NormalizedKline, start time.Time) error {
	if start.IsZero() || c.SupportsArbitraryRange {
		return nil
	}
	if len(rows) == 0 {
		return fmt.Errorf("%w: provider returned no rows to prove bounded history coverage", ErrHistoryCoverage)
	}
	oldest, newest := rows[0].BarStart, rows[0].BarStart
	for _, row := range rows[1:] {
		if row.BarStart.Before(oldest) {
			oldest = row.BarStart
		}
		if row.BarStart.After(newest) {
			newest = row.BarStart
		}
	}
	if oldest.After(start) || newest.Before(start) {
		return fmt.Errorf("%w: provider response does not prove coverage at %s (range %s..%s)", ErrHistoryCoverage, start.UTC().Format(time.RFC3339Nano), oldest.UTC().Format(time.RFC3339Nano), newest.UTC().Format(time.RFC3339Nano))
	}
	return nil
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
	ID              string
	SourceID        string
	DisplayName     string
	Hosts           []string
	ProtocolVariant string
	Transport       string
	Port            int
	Status          SourceStatus
}

var providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (d ProviderDescriptor) Validate() error {
	if !providerIDPattern.MatchString(strings.TrimSpace(d.ID)) {
		return fmt.Errorf("invalid provider id %q", d.ID)
	}
	if sourceID := strings.TrimSpace(d.SourceID); sourceID != "" && !providerIDPattern.MatchString(sourceID) {
		return fmt.Errorf("invalid source id %q", d.SourceID)
	}
	if strings.TrimSpace(d.DisplayName) == "" {
		return fmt.Errorf("display_name is required")
	}
	if len(d.Hosts) == 0 {
		return fmt.Errorf("hosts are required")
	}
	switch d.Status {
	case "", SourceEnabled, SourceShadow, SourceCatalogOnly:
	default:
		return fmt.Errorf("invalid source status %q", d.Status)
	}
	if d.Transport != "" && (d.Port < 1 || d.Port > 65535) {
		return fmt.Errorf("invalid source port %d", d.Port)
	}
	for _, host := range d.Hosts {
		if strings.TrimSpace(host) == "" || strings.Contains(host, " ") {
			return fmt.Errorf("invalid host %q", host)
		}
	}
	return nil
}

func (d ProviderDescriptor) SourceKey() SourceKey {
	return SourceKey{ProviderID: strings.TrimSpace(d.ID), SourceID: strings.TrimSpace(d.SourceID)}
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
	SourceID       string
	Frequency      string
	Limit          int
	StartTime      time.Time
	EndTime        time.Time
	Now            time.Time
	// HistoryAsOf is an explicit reference point for bounded historical
	// probes. It is normally zero and falls back to Now; deployment canaries
	// set it to the selected closed session so a weekend does not make a
	// known latest-page response look out of range.
	HistoryAsOf time.Time
	RequestID   string
	DNSRoutes   map[string][]string
	// RateBudgetRatio applies the HistoryPolicy budget to this invocation's
	// provider limiter. Zero means the normal provider budget (1.0).
	RateBudgetRatio float64
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
	if math.IsNaN(r.RateBudgetRatio) || math.IsInf(r.RateBudgetRatio, 0) || r.RateBudgetRatio < 0 || r.RateBudgetRatio > 1 {
		return fmt.Errorf("%w: rate_budget_ratio must be between 0 and 1", ErrInvalidRequest)
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
	DNSRoutes  map[string][]string
}

type InstrumentFetcher interface {
	MarketProvider
	InstrumentSpec() InstrumentSpec
	FetchInstrumentSnapshot(context.Context, InstrumentRequest) (InstrumentSnapshot, error)
}

type Instrument struct {
	SubjectID       string
	CanonicalSymbol string
	ProviderSymbol  string
	Exchange        string
	Name            string
	Status          string
	BaseAsset       string
	QuoteAsset      string
	MinQty          string
	MaxQty          string
	TickSize        string
	LotSize         string
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
