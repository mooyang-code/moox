package unitruntime

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// 主机重启后，run/pids 里的进程记录与 run/draining 里的停止中标记都属于上一次开机：进程必然已经不在，
// 但记录里的 PID 可能被别的进程复用（身份核对失败，守护不拉起），停止中标记也会一直挡住守护。
// 每条记录都写着启动时的内核 boot ID，与当前不同即属于上一次开机，持锁清理。

// 读取内核 boot ID；测试可替换。
var kernelBootID = readKernelBootID

// BootRecovery 是一条因主机重启而清理掉的残留：Kind 为 "pid"（进程记录）或 "draining"（停止被关机打断）。
type BootRecovery struct {
	Component string `json:"component"`
	Kind      string `json:"kind"`
}

// reconcileBoot 必须在持有维护锁之后调用。读不懂的记录留给身份核对报错，不擅自删除。
func (s *runtimeState) reconcileBoot() ([]BootRecovery, error) {
	current, err := kernelBootID()
	if err != nil {
		return nil, err
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
		if privateJSON(filepath.Join(s.pids.Name(), name), &record) != nil || record.KernelBootID == current {
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
		if privateJSON(filepath.Join(s.draining.Name(), name), &drain) != nil || drain.KernelBootID == current {
			continue
		}
		if err := removeState(s.draining, name); err != nil {
			return nil, err
		}
		recovered = append(recovered, BootRecovery{Component: name, Kind: "draining"})
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
