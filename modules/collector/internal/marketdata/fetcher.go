package marketdata

import (
	"context"
	"errors"
)

// KlineFetcher retrieves bars from one concrete source. Implementations own
// wire encoding and source-specific pagination; callers only see canonical
// marketdata values.
type KlineFetcher interface {
	Descriptor() ProviderDescriptor
	KlineSpec() KlineSpec
	FetchKlines(ctx context.Context, request KlineRequest) ([]NormalizedKline, error)
}

// InstrumentFetcher retrieves the source's instrument snapshot or page.
type InstrumentFetcher interface {
	Descriptor() ProviderDescriptor
	InstrumentSpec() InstrumentSpec
	FetchInstruments(ctx context.Context, request InstrumentRequest) (InstrumentSnapshot, error)
}

// MarketProvider is the registration unit for a source that can expose one or
// both of the shared market-data fetcher contracts.
type MarketProvider interface {
	Descriptor() ProviderDescriptor
}

var (
	ErrTimeout      = errors.New("marketdata: request timed out")
	ErrUnavailable  = errors.New("marketdata: source unavailable")
	ErrMalformed    = errors.New("marketdata: malformed source response")
	ErrRateLimited  = errors.New("marketdata: source rate limited")
	ErrOutOfRange   = errors.New("marketdata: request is outside source coverage")
	ErrNotSupported = errors.New("marketdata: operation is not supported")
)
