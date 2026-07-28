package exchange

import (
	"errors"
	"fmt"
	"strings"
)

type ErrorKind string

const (
	ErrorRejected            ErrorKind = "REJECTED"
	ErrorInsufficientBalance ErrorKind = "INSUFFICIENT_BALANCE"
	ErrorRateLimited         ErrorKind = "RATE_LIMITED"
	ErrorOrderNotFound       ErrorKind = "ORDER_NOT_FOUND"
	ErrorAuthentication      ErrorKind = "AUTHENTICATION"
	ErrorNotReady            ErrorKind = "NOT_READY"
	ErrorTransportUnknown    ErrorKind = "TRANSPORT_UNKNOWN"
)

type Error struct {
	Kind       ErrorKind
	HTTPStatus int
	Code       string
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := string(e.Kind)
	details := make([]string, 0, 2)
	if e.HTTPStatus != 0 {
		details = append(details, fmt.Sprintf("HTTP %d", e.HTTPStatus))
	}
	if e.Code != "" {
		details = append(details, "code "+e.Code)
	}
	if len(details) != 0 {
		message += " (" + strings.Join(details, ", ") + ")"
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsKind(err error, kind ErrorKind) bool {
	var exchangeErr *Error
	return errors.As(err, &exchangeErr) && exchangeErr.Kind == kind
}
