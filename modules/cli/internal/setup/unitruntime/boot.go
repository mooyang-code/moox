package unitruntime

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 主机重启后，run/pids 里的进程记录与 run/draining 里的停止中标记都属于上一次开机：进程必然已经不在，
// 但记录里的 PID 可能被别的进程复用（身份核对失败，守护不拉起），停止中标记也会一直挡住守护。
// run/boot.json 记下上次见到的内核 boot ID；不同即说明重启过，持锁清理上次开机的残留。
// 不往 PID 记录里加字段：旧版 runtime 严格解析记录，回滚后仍能读取（它会忽略 boot.json）。

const bootStateName = "boot.json"

// bootClockSkew 是没有 boot.json 时（新版本第一次运行）按开机时间判断的余量：开机后时钟被校准回拨，
// 本次开机启动的进程也可能早于内核记录的开机时间几秒，留出余量避免把正在运行的进程当成残留。
const bootClockSkew = time.Minute

// 读取内核 boot ID 与开机时间；测试可替换。
var (
	kernelBootID   = readKernelBootID
	systemBootTime = readSystemBootTime
)

type bootState struct {
	KernelBootID string    `json:"kernel_boot_id"`
	RecordedAt   time.Time `json:"recorded_at"`
}

// BootRecovery 是一条因主机重启而清理掉的残留：Kind 为 "pid"（进程记录）或 "draining"（停止被关机打断）。
type BootRecovery struct {
	Component string `json:"component"`
	Kind      string `json:"kind"`
}

// reconcileBoot 必须在持有维护锁之后调用。boot ID 与上次相同时什么也不做。
func (s *runtimeState) reconcileBoot() ([]BootRecovery, error) {
	current, err := kernelBootID()
	if err != nil {
		return nil, err
	}
	var previous bootState
	err = privateJSON(filepath.Join(s.run.Name(), bootStateName), &previous)
	switch {
	case err == nil && previous.KernelBootID == current:
		return nil, nil
	case err != nil && !os.IsNotExist(err):
		return nil, err
	}
	rebooted := err == nil // 有记录且不同：确定重启过
	var bootedAt time.Time
	if !rebooted {
		// 没有 boot.json：只把明显早于本次开机的记录当成残留。
		if bootedAt, err = systemBootTime(); err != nil {
			return nil, err
		}
	}
	stale := func(startedAt time.Time) bool {
		return rebooted || (!startedAt.IsZero() && startedAt.Before(bootedAt.Add(-bootClockSkew)))
	}
	var recovered []BootRecovery
	pidNames, err := stateNames(s.pids)
	if err != nil {
		return nil, err
	}
	for _, name := range pidNames {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		var record pidRecord
		readErr := privateJSON(filepath.Join(s.pids.Name(), name), &record)
		if readErr != nil && !rebooted {
			continue // 读不懂的记录交给原有的身份核对报错，不擅自删除
		}
		if readErr == nil && !stale(record.StartedAt) {
			continue
		}
		if err := removeState(s.pids, name); err != nil {
			return nil, err
		}
		recovered = append(recovered, BootRecovery{Component: strings.TrimSuffix(name, ".json"), Kind: "pid"})
	}
	drainNames, err := stateNames(s.draining)
	if err != nil {
		return nil, err
	}
	for _, name := range drainNames {
		var drain drainRecord
		readErr := privateJSON(filepath.Join(s.draining.Name(), name), &drain)
		if readErr != nil && !rebooted {
			continue
		}
		if readErr == nil && !stale(drain.StartedAt) {
			continue
		}
		if err := removeState(s.draining, name); err != nil {
			return nil, err
		}
		recovered = append(recovered, BootRecovery{Component: name, Kind: "draining"})
	}
	if err := writeState(s.run, bootStateName, bootState{KernelBootID: current, RecordedAt: time.Now().UTC()}); err != nil {
		return nil, err
	}
	sort.Slice(recovered, func(i, j int) bool {
		if recovered[i].Component != recovered[j].Component {
			return recovered[i].Component < recovered[j].Component
		}
		return recovered[i].Kind < recovered[j].Kind
	})
	return recovered, nil
}

// stateNames 列出状态目录里的普通文件（跳过写入中的临时文件）。
func stateNames(root *os.Root) ([]string, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || !entry.Type().IsRegular() {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

var errBootUnsupported = errors.New("kernel boot identity requires Linux")
