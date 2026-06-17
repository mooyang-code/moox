package errors

import "errors"

// 通用错误
var (
	ErrUnknownEventType    = errors.New("unknown event type")
	ErrInvalidConfig       = errors.New("invalid configuration")
	ErrNodeNotFound        = errors.New("node not found")
	ErrTaskNotFound        = errors.New("task not found")
	ErrCollectorNotFound   = errors.New("collector not found")
	ErrInvalidTaskType     = errors.New("invalid task type")
	ErrTaskAlreadyRunning  = errors.New("task already running")
	ErrTaskNotRunning      = errors.New("task not running")
	ErrInvalidSchedule     = errors.New("invalid schedule expression")
	ErrCollectFailed       = errors.New("data collection failed")
	ErrHeartbeatFailed     = errors.New("heartbeat report failed")
	ErrConfigSyncFailed    = errors.New("config synchronization failed")
	ErrStorageNotAvailable = errors.New("storage not available")
)

// 自定义错误类型
type AppError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Cause   error  `json:"-"`
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Cause
}

// 错误构造函数
func NewAppError(code, message string, cause error) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// 预定义错误代码
const (
	ErrCodeInvalidRequest   = "INVALID_REQUEST"
	ErrCodeTaskError        = "TASK_ERROR"
	ErrCodeCollectorError   = "COLLECTOR_ERROR"
	ErrCodeConfigError      = "CONFIG_ERROR"
	ErrCodeHeartbeatError   = "HEARTBEAT_ERROR"
	ErrCodeStorageError     = "STORAGE_ERROR"
	ErrCodeNetworkError     = "NETWORK_ERROR"
	ErrCodeInternalError    = "INTERNAL_ERROR"
)