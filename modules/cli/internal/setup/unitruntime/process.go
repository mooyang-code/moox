package unitruntime

import "errors"

var errIdentity = errors.New("runtime process identity does not match its PID record; refusing to signal it")

type processIdentity struct {
	PID        int    `json:"pid"`
	StartTicks string `json:"start_ticks"`
	Executable string `json:"executable"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
	UID        uint32 `json:"uid"`
}
