package pebble

import (
	"errors"
	"fmt"

	cpebble "github.com/cockroachdb/pebble"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
// relay 把它隔离（记日志后删除），不能让一条坏记录堵住同一 DataNode 其后的全部事件。
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

// ErrorCode 把存储层错误映射成对外错误码：参数错误、不存在、冲突，其余都是内部错误。
func ErrorCode(err error) pb.ErrorCode {
	if err == nil {
		return pb.ErrorCode_SUCCESS
	}
	if errors.Is(err, cpebble.ErrNotFound) {
		return pb.ErrorCode_NOT_FOUND
	}
	var (
		validation         ValidationError
		conflict           ConflictError
		commitConflict     CommitConflictError
		periodConflict     PeriodConflictError
		staleEffectiveFrom StaleEffectiveFromError
		hashMismatch       PeriodHashMismatchError
		snapshotMissing    SeriesSnapshotMissingError
		deadlinePassed     PeriodDeadlinePassedError
	)
	switch {
	case errors.As(err, &validation), errors.As(err, &deadlinePassed):
		return pb.ErrorCode_INVALID_PARAM
	case errors.As(err, &conflict), errors.As(err, &commitConflict), errors.As(err, &periodConflict),
		errors.As(err, &staleEffectiveFrom), errors.As(err, &hashMismatch):
		return pb.ErrorCode_CONFLICT
	case errors.As(err, &snapshotMissing):
		return pb.ErrorCode_NOT_FOUND
	default:
		return pb.ErrorCode_INNER_ERR
	}
}
