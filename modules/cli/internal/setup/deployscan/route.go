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
	AdminReady    bool
	ControlReady  bool
	OperatorPin   bool
	UnitRecords   bool
	// WipePending 表示上次 setup wipe 没有做完。
	WipePending bool
	// StaleBinding 表示这份本机状态不是当前配置和版本生成的：旧状态里的软件和身份绑定着原来的配置，不能接着装。
	StaleBinding bool
	// BindingConfig 是生成这份本机状态的配置文件路径。
	BindingConfig string
	// Units 是已经部署成功的单元，键是 主机/profile。
	Units map[string]bool
	// InitStorage、InitReady 表示 setup init --stage storage、setup init 已完成。
	InitStorage bool
	InitReady   bool
	// Started 是已启动的空间，见 setup start。
	Started map[string]bool
	// Verified 是验收通过的空间，见 setup verify。
	Verified map[string]bool
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
	// 干净的机器上也可能有目录外的进程，要在判断空机之前处理，由用户确认后结束。
	for _, report := range reports {
		if len(report.ProcessesOutside) != 0 {
			return Decision{Route: RouteExisting, Reasons: []string{fmt.Sprintf("%s（%s）：部署目录之外还有 MooX 进程：%v，确认后用 setup wipe --kill-outside 结束", report.Host, report.Address, report.ProcessesOutside)}}
		}
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
			if local.StaleBinding {
				return Decision{Route: RouteExisting, Reasons: []string{staleReason(local)}}
			}
			return Decision{Route: RouteResume}
		}
		if matched && local.AdminReady && local.OperatorPin && local.UnitRecords {
			return Decision{Route: RouteUpgrade}
		}
	}
	return Decision{Route: RouteExisting}
}

func staleReason(local LocalState) string {
	reason := "上次没装完的状态是另一份配置或另一个版本生成的，不能接着装"
	if local.BindingConfig != "" {
		reason += "（原配置在 " + local.BindingConfig + "）"
	}
	return reason + "。要继续就换回原配置和原版本，否则彻底清空后重新安装"
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
		if report.HelperRunning {
			add(report, "上一次部署操作还在运行，等它结束再继续")
		}
		if len(report.PortsOccupied) != 0 {
			add(report, "这些端口被其他程序占用：%v", report.PortsOccupied)
		}
	}
	return reasons
}
