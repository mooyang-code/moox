package deployscan

import (
	"fmt"
	"slices"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

// UnfinishedReason 处理升级路线上配置或版本变了的情况：可能是安装中途换的，也可能是装好后有意加了主机或组件，
// 两者都让 NextSteps 为空，只说事实，由用户分辨。早期部署没有记录，按正常升级走。
func UnfinishedReason(manifest setupconfig.Manifest, route string, local LocalState) string {
	if route != RouteUpgrade || !local.StaleBinding || local.BindingConfig == "" {
		return ""
	}
	local.StaleBinding = false
	// 验收是安装后的检查，没有记录不代表部署没做完。
	pending := slices.DeleteFunc(NextSteps(manifest, route, local), func(step string) bool {
		return strings.Contains(step, "setup verify") || strings.Contains(step, "setup start")
	})
	if len(pending) == 0 {
		return ""
	}
	return "按当前配置，这些步骤没有完成记录：" + strings.Join(pending, "；") + "。如果是安装中途换了配置或版本，换回原配置和原版本（原配置在 " + local.BindingConfig + "）后补上；如果是装好后有意新增主机或组件，按 host-registry.md 部署，不要清空"
}

// 步骤类型。
const (
	StepFirewall    = "firewall"
	StepAdmin       = "admin"
	StepHost        = "host"
	StepUnit        = "unit"
	StepInitStorage = "init_storage"
	StepControl     = "control"
	StepInit        = "init"
	StepBackup      = "backup"
	StepStart       = "start"
	StepVerify      = "verify"
)

// Step 是一个安装步骤。Group 相同的步骤可以并行，同一主机串行；Command 是手工运行的命令。
type Step struct {
	Kind    string `json:"kind"`
	Host    string `json:"host,omitempty"`
	Unit    string `json:"unit,omitempty"`
	Space   string `json:"space,omitempty"`
	Group   int    `json:"group"`
	Command string `json:"command"`
}

// NextSteps 按当前拓扑和已完成的步骤列出接下来要运行的命令。顺序只有 PlanSteps 一个来源，文档和 skill 都照它走。
func NextSteps(manifest setupconfig.Manifest, route string, local LocalState) []string {
	steps := []string{}
	for _, step := range PlanSteps(manifest, route, local, "") {
		steps = append(steps, step.Command)
	}
	return steps
}

// PlanSteps 返回还没完成的步骤。顺序规则：组件的就绪条件不能依赖排在它后面的步骤。Monitor 就绪要求系统元数据已激活，
// 所以元数据排在管理台之前；Access 就绪只看目录，出口代理没有依赖，都可以排在元数据之前。
// 只有首次安装的路线才有步骤；旧状态和其他路线返回空。space 为空时启动步骤留占位，已启动的空间才验收。
func PlanSteps(manifest setupconfig.Manifest, route string, local LocalState, space string) []Step {
	switch route {
	case RouteFresh:
		local = LocalState{Units: map[string]bool{}}
	case RouteResume, RouteUpgrade:
		if local.StaleBinding {
			return []Step{}
		}
	default:
		return []Step{}
	}
	steps := []Step{}
	group := 0
	add := func(done bool, step Step, format string, args ...any) {
		if !done {
			step.Group, step.Command = group, "./bin/moox-cli "+fmt.Sprintf(format, args...)
			steps = append(steps, step)
		}
	}
	control := manifest.ControlHost().Name
	storage := manifest.ComponentHost("storage-primary")
	hostsWith := func(component string) []string {
		hosts := []string{}
		for _, host := range manifest.Hosts() {
			if slices.Contains(manifest.Deployments[host.Name], component) {
				hosts = append(hosts, host.Name)
			}
		}
		return hosts
	}
	unit := func(profile, component string) {
		for _, host := range hostsWith(component) {
			add(local.Units[host+"/"+profile], Step{Kind: StepUnit, Host: host, Unit: profile}, "setup deploy-unit --host %s --profile %s", host, profile)
		}
	}
	add(local.AdminReady, Step{Kind: StepFirewall, Host: control}, "setup firewall --yes")
	group++
	add(local.AdminReady, Step{Kind: StepAdmin, Host: control}, "setup bootstrap --stage admin")
	group++
	for _, host := range manifest.Hosts() {
		if host.Name != control {
			add(local.Units[host.Name+"/host"], Step{Kind: StepHost, Host: host.Name}, "setup deploy-host --host %s", host.Name)
		}
	}
	group++
	unit("eventbus", "eventbus")
	group++
	unit("storage", "storage-primary")
	unit("access", "access")
	unit("egress-proxy", "egress-proxy")
	group++
	add(local.InitStorage, Step{Kind: StepInitStorage, Host: storage}, "setup init --stage storage --config-dir ./config/setup --storage-host %s", storage)
	group++
	add(local.ControlReady, Step{Kind: StepControl, Host: control}, "setup bootstrap --stage control")
	group++
	add(local.InitReady, Step{Kind: StepInit, Host: control}, "setup init --config-dir ./config/setup --storage-host %s", storage)
	group++
	unit("collector-stream", "collector-stream")
	unit("trade", "trade")
	unit("factor-engine", "factor-engine")
	unit("archive-worker", "archive-worker")
	group++
	spaces := CollectSpaces(manifest)
	started := slices.DeleteFunc(slices.Clone(spaces), func(item string) bool { return !local.Started[item] })
	if space != "" {
		add(local.Started[space], Step{Kind: StepStart, Space: space, Host: control}, "setup start --space-id %s", space)
		if !local.Started[space] {
			started = append(started, space)
		}
	} else {
		// 启动哪个空间由用户选，手工路径留占位，setup install 用参数填入。
		add(len(spaces) == 0 || len(started) != 0, Step{Kind: StepStart, Host: control}, "setup start --space-id <空间>")
	}
	group++
	// setup install 用记录判断，备份失败后重跑能补上；手工路径 config backup 可以重复运行，只在还有步骤时列出。
	backedUp := len(steps) == 0
	if space != "" {
		backedUp = local.BackedUp
	}
	add(backedUp, Step{Kind: StepBackup, Host: control}, "config backup")
	group++
	for _, item := range slices.Compact(started) {
		add(local.Verified[item], Step{Kind: StepVerify, Space: item, Host: control}, "setup verify --space-id %s", item)
		group++
	}
	return steps
}

// ManualSteps 列出要用户自己做的事。浏览器信任要这台电脑的管理员授权，不放进 NextSteps，免得 Agent 卡在这里。
func ManualSteps(route string) []string {
	if route != RouteFresh && route != RouteResume {
		return nil
	}
	return []string{"管理台装好后，在自己的终端运行 ./bin/moox-cli setup trust-browser。不做时管理台能打开，但浏览器有安全提示"}
}

// CollectSpaces 返回这个拓扑能采集的空间：有 collector-stream 就有 crypto，再加配了云函数的空间。
func CollectSpaces(manifest setupconfig.Manifest) []string {
	spaces := []string{}
	if manifest.ComponentHost("collector-stream") != "" {
		spaces = append(spaces, "crypto")
	}
	if manifest.SCFEnabled() {
		for _, space := range manifest.SCFPlan.Spaces {
			spaces = append(spaces, space.SpaceID)
		}
	}
	slices.Sort(spaces)
	return slices.Compact(spaces)
}
