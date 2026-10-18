package unitruntime

import "errors"

var errIdentity = errors.New("runtime process identity does not match its PID record; refusing to signal it")

// IsIdentityError 报告 err 是否为进程身份与 PID 记录不符（运行助手拒绝操作该进程，需要人工处理）。
func IsIdentityError(err error) bool { return errors.Is(err, errIdentity) }

type processIdentity struct {
	PID        int    `json:"pid"`
	StartTicks string `json:"start_ticks"`
	Executable string `json:"executable"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
	UID        uint32 `json:"uid"`
}
