package exchange

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type ErrorCategory string

const (
	ErrorInvalidRequest      ErrorCategory = "INVALID_REQUEST"
	ErrorInsufficientBalance ErrorCategory = "INSUFFICIENT_BALANCE"
	ErrorRuleViolation       ErrorCategory = "RULE_VIOLATION"
	ErrorRateLimited         ErrorCategory = "RATE_LIMITED"
	ErrorTimeSkew            ErrorCategory = "TIME_SKEW"
	ErrorAuthFailed          ErrorCategory = "AUTH_FAILED"
	ErrorRejected            ErrorCategory = "EXCHANGE_REJECTED"
	ErrorTransportUncertain  ErrorCategory = "TRANSPORT_UNCERTAIN"
	ErrorTransient           ErrorCategory = "TRANSIENT_UNAVAILABLE"
	ErrorPermanent           ErrorCategory = "PERMANENT_FAILURE"
)

type ClassifiedError struct {
	Category ErrorCategory
	Err      error
}

func (e *ClassifiedError) Error() string { return string(e.Category) + ": " + e.Err.Error() }
func (e *ClassifiedError) Unwrap() error { return e.Err }
func IsCategory(err error, c ErrorCategory) bool {
	var e *ClassifiedError
	return errors.As(err, &e) && e.Category == c
}

type PlaceRequest struct {
	ClientOrderID, Symbol, Side, Type, TimeInForce string
	Quantity, Price                                shared.Decimal
	ReduceOnly                                     bool
}
type ExchangeOrderResult struct {
	ExchangeOrderID, ClientOrderID, Status string
	FilledQuantity                         shared.Decimal
}
type FillEvent struct {
	ExchangeTradeID, ExchangeOrderID, ClientOrderID, Symbol, Side, BaseAsset, QuoteAsset string
	Quantity, Price, Fee                                                                 shared.Decimal
	FeeCurrency                                                                          string
}
type PrivateEventHandler func(context.Context, FillEvent) error

type TradingAdapter interface {
	Place(context.Context, PlaceRequest) (ExchangeOrderResult, error)
	Cancel(context.Context, string, string) (ExchangeOrderResult, error)
	QueryByClientOrderID(context.Context, string, string) (ExchangeOrderResult, error)
	Rules(context.Context, string) (instrument.Rules, error)
	SubscribePrivate(context.Context, PrivateEventHandler) error
}
