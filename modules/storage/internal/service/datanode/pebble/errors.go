package pebble

import (
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/storeerr"
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

var ErrOutboxEntryNotFound = errors.New("outbox entry not found")

// ErrInvalidOutboxEvent 标记无法通过当前事件契约校验的出站记录：它永远发不出去，
// relay 把它隔离（记日志后删除），不能让一条坏记录堵住其后的全部事件。
var ErrInvalidOutboxEvent = errors.New("invalid outbox event")

// IsInvalidOutboxEvent 判断错误是否为无法通过契约校验的出站记录。
func IsInvalidOutboxEvent(err error) bool {
	return errors.Is(err, ErrInvalidOutboxEvent)
}

// ErrDatasetDeleted 表示写入拿到数据集读锁后，元数据里已经没有这个数据集：删除先删元数据、再在写锁里删行，
// 写锁之后才进来的写入在这里被拒绝，不会留下孤儿行。
var ErrDatasetDeleted = errors.New("dataset is deleted")

type ConflictError struct{ EventID string }

func (e ConflictError) Error() string {
	return fmt.Sprintf("dataset marker event_id %q already exists with a different payload", e.EventID)
}

type CommitConflictError struct{ CommitID string }

func (e CommitConflictError) Error() string {
	return fmt.Sprintf("commit_id %q already exists with a different payload", e.CommitID)
}

type PeriodConflictError struct {
	SpaceID, DatasetID, Frequency string
	PeriodTime                    int64
}

func (e PeriodConflictError) Error() string {
	return fmt.Sprintf("dataset period %s/%s/%s/%d already exists with a different expectation", e.SpaceID, e.DatasetID, e.Frequency, e.PeriodTime)
}

// 存储层错误的类别，由 retinfo.Code 统一映射成错误码。
func (ValidationError) StoreErrorKind() storeerr.Kind     { return storeerr.Invalid }
func (ConflictError) StoreErrorKind() storeerr.Kind       { return storeerr.Conflict }
func (CommitConflictError) StoreErrorKind() storeerr.Kind { return storeerr.Conflict }
func (PeriodConflictError) StoreErrorKind() storeerr.Kind { return storeerr.Conflict }
