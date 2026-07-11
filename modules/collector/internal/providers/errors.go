package providers

import (
	"errors"
	"fmt"
	"time"
)

type ErrorKind string

const (
	ErrorRateLimited            ErrorKind = "rate_limited"
	ErrorUnauthorized           ErrorKind = "unauthorized"
	ErrorTemporarilyUnavailable ErrorKind = "temporarily_unavailable"
	ErrorUnsupported            ErrorKind = "unsupported"
	ErrorParseFailed            ErrorKind = "parse_failed"
)

type Error struct {
	Kind       ErrorKind
	Message    string
	RetryAfter time.Time
	Cause      error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}
func (e *Error) Unwrap() error { return e.Cause }
func (e *Error) Retryable() bool {
	return e.Kind == ErrorRateLimited || e.Kind == ErrorTemporarilyUnavailable
}
func NewError(kind ErrorKind, message string, cause error) error {
	return &Error{Kind: kind, Message: message, Cause: cause}
}
func IsKind(err error, kind ErrorKind) bool {
	var target *Error
	return errors.As(err, &target) && target.Kind == kind
}
