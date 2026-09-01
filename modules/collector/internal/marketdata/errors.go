package marketdata

import (
	"context"
	"errors"
	"time"
)

var (
	ErrProviderAlreadyRegistered = errors.New("provider already registered")
	ErrProviderNotFound          = errors.New("provider not found")
	ErrFetcherNotSupported       = errors.New("fetcher not supported")
	ErrTimeout                   = errors.New("timeout")
	ErrRateLimited               = errors.New("rate limited")
	ErrHTTPStatus                = errors.New("http status")
	ErrProtocol                  = errors.New("protocol error")
	ErrNoClosedBar               = errors.New("no closed bar")
	ErrUnsupportedSymbol         = errors.New("unsupported symbol")
	ErrUnsupportedFrequency      = errors.New("unsupported frequency")
	ErrHistoryOutOfRange         = errors.New("history request out of provider range")
	ErrHistoryCoverage           = errors.New("history response coverage is unverified")
	ErrInvalidRequest            = errors.New("invalid request")
)

type ErrorKind string

const (
	ErrorKindUnknown              ErrorKind = "unknown"
	ErrorKindTimeout              ErrorKind = "timeout"
	ErrorKindRateLimited          ErrorKind = "rate_limited"
	ErrorKindHTTPStatus           ErrorKind = "http_status"
	ErrorKindProtocol             ErrorKind = "protocol"
	ErrorKindNoClosedBar          ErrorKind = "no_closed_bar"
	ErrorKindUnsupportedSymbol    ErrorKind = "unsupported_symbol"
	ErrorKindUnsupportedFrequency ErrorKind = "unsupported_frequency"
	ErrorKindInvalidRequest       ErrorKind = "invalid_request"
	ErrorKindHistoryOutOfRange    ErrorKind = "history_out_of_range"
	ErrorKindHistoryCoverage      ErrorKind = "history_coverage"
	ErrorKindContextCanceled      ErrorKind = "context_canceled"
	ErrorKindDeadlineExceeded     ErrorKind = "deadline_exceeded"
)

func ClassifyError(err error) ErrorKind {
	switch {
	case err == nil:
		return ErrorKindUnknown
	case errors.Is(err, context.Canceled):
		return ErrorKindContextCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorKindDeadlineExceeded
	case errors.Is(err, ErrTimeout):
		return ErrorKindTimeout
	case errors.Is(err, ErrRateLimited):
		return ErrorKindRateLimited
	case errors.Is(err, ErrHTTPStatus):
		return ErrorKindHTTPStatus
	case errors.Is(err, ErrProtocol):
		return ErrorKindProtocol
	case errors.Is(err, ErrNoClosedBar):
		return ErrorKindNoClosedBar
	case errors.Is(err, ErrUnsupportedSymbol):
		return ErrorKindUnsupportedSymbol
	case errors.Is(err, ErrUnsupportedFrequency):
		return ErrorKindUnsupportedFrequency
	case errors.Is(err, ErrInvalidRequest):
		return ErrorKindInvalidRequest
	case errors.Is(err, ErrHistoryOutOfRange):
		return ErrorKindHistoryOutOfRange
	case errors.Is(err, ErrHistoryCoverage):
		return ErrorKindHistoryCoverage
	default:
		return ErrorKindUnknown
	}
}

func CanFallback(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil {
		if ctx.Err() != nil {
			return false
		}
		if deadline, ok := ctx.Deadline(); ok && !deadline.After(time.Now()) {
			return false
		}
	}
	switch ClassifyError(err) {
	case ErrorKindTimeout, ErrorKindRateLimited, ErrorKindHTTPStatus, ErrorKindProtocol, ErrorKindNoClosedBar, ErrorKindHistoryCoverage:
		return true
	default:
		return false
	}
}
