package deployscan

import (
	"fmt"
	"slices"
)

// 发布路线。
const (
	RouteFresh    = "fresh"    // 全是空机，全新安装
	RouteResume   = "resume"   // 这台电脑上次的首次安装没做完，继续安装
	RouteExisting = "existing" // 已有部署，本机没有匹配的状态（新电脑都是这种）
	RouteUpgrade  = "upgrade"  // 已有部署，本机状态完整（原电脑）
	RouteBlocked  = "blocked"  // 有阻塞项，不能清除也不能安装
)

// LocalState 是这台电脑上的部署状态，只读取，不创建。
type LocalState struct {
	// RuntimeSHA256 是本机身份的绑定哈希；为空表示没有本机状态。
	RuntimeSHA256 string
	CoreReady     bool
	ControlReady  bool
	OperatorPin   bool
	UnitRecords   bool
	// WipePending 表示上次 setup wipe 没有做完。
	WipePending bool
}

// Decision 是路线判定的结果。
type Decision struct {
	Route   string   `json:"route"`
	Reasons []string `json:"reasons,omitempty"`
}

// Decide 判定发布路线，顺序不能调换：阻塞项优先，未完成的清除其次。
func Decide(reports []HostReport, control string, local LocalState) Decision {
	if reasons := blockers(reports); len(reasons) != 0 {
		return Decision{Route: RouteBlocked, Reasons: reasons}
	}
	if local.WipePending {
		return Decision{Route: RouteExisting, Reasons: []string{"上次 setup wipe 没有做完，重新运行 setup wipe"}}
	}
	empty, deployed := true, false
	remote := ""
	for _, report := range reports {
		empty = empty && report.Empty()
		deployed = deployed || len(report.Units) != 0
		if report.Host == control {
			remote = report.RuntimeSHA256
		}
	}
	if empty {
		return Decision{Route: RouteFresh}
	}
	if local.RuntimeSHA256 != "" {
		matched := remote == local.RuntimeSHA256
		// 首次安装在写身份绑定之前就失败时，目标机只有上传目录，也算没做完。
		unfinished := matched || (remote == "" && !deployed)
		if unfinished && !local.ControlReady {
			return Decision{Route: RouteResume}
		}
		if matched && local.CoreReady && local.OperatorPin && local.UnitRecords {
			return Decision{Route: RouteUpgrade}
		}
	}
	return Decision{Route: RouteExisting}
}

func blockers(reports []HostReport) []string {
	var reasons []string
	add := func(report HostReport, format string, args ...any) {
		reasons = append(reasons, fmt.Sprintf("%s（%s）：", report.Host, report.Address)+fmt.Sprintf(format, args...))
	}
	for _, report := range reports {
		if !report.OwnerOK {
			add(report, "部署目录不属于这次配置的 SSH 用户，旧的看护任务和服务在原用户名下，看不到也清不掉。请改用原部署用户")
		}
		if !report.ModeOK {
			add(report, "部署目录不是普通目录，或者组和其他用户可写")
		}
		if !report.ParentWritable {
			add(report, "部署目录不存在，上级目录也不可写")
		}
		if report.ForeignFiles {
			add(report, "部署目录里有不属于 SSH 用户的文件，清除时删不掉")
		}
		for _, dir := range append(slices.Clone(report.ExternalDirs), report.ArchiveDirs...) {
			if dir.Marked && dir.Foreign {
				add(report, "目录 %s 里有不属于 SSH 用户的文件，清除时删不掉", dir.Path)
			}
		}
		if len(report.ProcessesOutside) != 0 {
			add(report, "部署目录之外还有 MooX 进程：%v", report.ProcessesOutside)
		}
		if len(report.PortsOccupied) != 0 {
			add(report, "这些端口被其他程序占用：%v", report.PortsOccupied)
		}
	}
	return reasons
}
