package providers

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type Feed string

const (
	FeedKline      Feed = "kline"
	FeedInstrument Feed = "instrument"
	FeedCalendar   Feed = "calendar"
)

type Capability struct {
	Feed           Feed
	ProductType    marketdata.ProductType
	InstrumentType marketdata.InstrumentType
	Frequency      marketdata.Frequency
	StartTime      time.Time
	EndTime        time.Time
	FeedScope      string
}
type CapabilityQuery struct {
	Feed           Feed
	ProductType    marketdata.ProductType
	InstrumentType marketdata.InstrumentType
	Frequency      marketdata.Frequency
	StartTime      time.Time
	EndTime        time.Time
	FeedScope      string
}

func (c Capability) Matches(q CapabilityQuery) bool {
	return c.Feed == q.Feed && c.ProductType == q.ProductType && c.InstrumentType == q.InstrumentType && c.Frequency == q.Frequency && (q.FeedScope == "" || c.FeedScope == q.FeedScope) && (c.StartTime.IsZero() || !q.StartTime.Before(c.StartTime)) && (c.EndTime.IsZero() || !q.EndTime.After(c.EndTime))
}

type ProviderSubject struct {
	SubjectID      string
	ProviderSymbol string
}
type SubjectResult struct {
	SubjectID string
	Status    string
	Error     string
}
type FetchKlinesRequest struct {
	MarketID       marketdata.MarketID
	ExchangeID     marketdata.ExchangeID
	ProductType    marketdata.ProductType
	InstrumentType marketdata.InstrumentType
	Frequency      marketdata.Frequency
	Subjects       []ProviderSubject
	StartTime      time.Time
	EndTime        time.Time
	Limit          int
	Cursor         string
}
type FetchKlinesResult struct {
	Rows           []marketdata.ProviderKline
	SubjectResults []SubjectResult
	NextCursor     string
	Complete       bool
	RequestCount   int
	Latency        time.Duration
}
type KlineProvider interface {
	ID() marketdata.ProviderID
	Capabilities() []Capability
	FetchKlines(context.Context, RequestGate, FetchKlinesRequest) (FetchKlinesResult, error)
}

type FetchInstrumentsRequest struct {
	MarketID        marketdata.MarketID
	ExchangeID      marketdata.ExchangeID
	InstrumentTypes []marketdata.InstrumentType
	SnapshotAt      time.Time
	Limit           int
	Cursor          string
}
type ProviderInstrument struct {
	SubjectID      string
	ProviderID     marketdata.ProviderID
	ProviderSymbol string
	ExchangeID     marketdata.ExchangeID
	ProductType    marketdata.ProductType
	InstrumentType marketdata.InstrumentType
	Name           string
	Currency       string
	ListingDate    string
	DelistingDate  string
	Status         string
	EffectiveAt    time.Time
	FetchedAt      time.Time
	RequestID      string
}
type ResolvedInstrument struct {
	ProviderInstrument
	SourceDatasetID string
	QualityStatus   string
	Generation      time.Time
	ResolvedAt      time.Time
}
type FetchInstrumentsResult struct {
	Instruments  []ProviderInstrument
	NextCursor   string
	Complete     bool
	RequestCount int
}
type InstrumentProvider interface {
	ID() marketdata.ProviderID
	Capabilities() []Capability
	FetchInstruments(context.Context, RequestGate, FetchInstrumentsRequest) (FetchInstrumentsResult, error)
}

type FetchCalendarRequest struct {
	MarketID   marketdata.MarketID
	ExchangeID marketdata.ExchangeID
	StartDate  string
	EndDate    string
	Limit      int
	Cursor     string
}
type ProviderSession struct {
	TradeDate   string
	OpenTime    time.Time
	CloseTime   time.Time
	Status      string
	EffectiveAt time.Time
}
type FetchCalendarResult struct {
	Sessions     []ProviderSession
	NextCursor   string
	Complete     bool
	RequestCount int
}
type CalendarProvider interface {
	ID() marketdata.ProviderID
	Capabilities() []Capability
	FetchCalendar(context.Context, RequestGate, FetchCalendarRequest) (FetchCalendarResult, error)
}
