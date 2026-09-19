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

// ErrUnsupportedOutboxEvent marks a persisted outbox payload whose event
// contract is no longer published. The relay drops these entries so a retired
// type cannot block DatasetRowsUpserted or the current period markers.
var ErrUnsupportedOutboxEvent = errors.New("unsupported outbox event")

// ErrDatasetDeleted is returned after the destructive dataset boundary has
// been crossed. The tombstone prevents a delayed writer from recreating rows
// after the metadata object has already been removed.
var ErrDatasetDeleted = errors.New("dataset is deleted")

func IsUnsupportedOutboxEvent(err error) bool {
	return errors.Is(err, ErrUnsupportedOutboxEvent)
}

type ConflictError struct{ EventID string }

func (e ConflictError) Error() string {
	return fmt.Sprintf("dataset marker event_id %q already exists with a different payload", e.EventID)
}

type CommitConflictError struct{ CommitID string }

func (e CommitConflictError) Error() string {
	return fmt.Sprintf("commit_id %q already exists with a different payload", e.CommitID)
}
