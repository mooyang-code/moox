package domain

import "time"

// 标签同步的运行结果取值。
const (
	TagSyncSuccess = "success"
	TagSyncFailed  = "failed"
)

// TagSyncRun 是一个标签上一次标的同步的运行结果。LastSuccessAt 为零表示从未成功过。
type TagSyncRun struct {
	SpaceID       string
	TagID         string
	LastRunAt     time.Time
	LastStatus    string
	LastError     string
	LastSuccessAt time.Time
}
