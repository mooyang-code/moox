package pebble

import (
	"errors"
	"fmt"
)

type ValidationError struct{ cause error }

func (e ValidationError) Error() string { return e.cause.Error() }
func (e ValidationError) Unwrap() error { return e.cause }

func invalid(message string) error {
	return ValidationError{cause: errors.New(message)}
}

func invalidf(format string, args ...any) error {
	return ValidationError{cause: fmt.Errorf(format, args...)}
}

func ValidationErrorFor(message string) error { return invalid(message) }

type ConflictError struct{ EventID string }

func (e ConflictError) Error() string {
	return fmt.Sprintf("dataset marker event_id %q already exists with a different payload", e.EventID)
}
