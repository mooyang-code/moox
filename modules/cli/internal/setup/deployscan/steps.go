package deployscan

import (
	"fmt"
	"slices"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

// NextSteps 按当前拓扑和已完成的步骤列出接下来要运行的命令。安装顺序只在这里写一份，文档和 skill 都照它走。
// 顺序规则：组件的就绪条件不能依赖排在它后面的步骤。Monitor 就绪要求系统元数据已激活，
// 所以 init --stage storage 排在管理台之前；Access 就绪只依赖服务目录，core 完成后目录已有全部部署。
// internalTLS 表示管理台用内部证书。只有首次安装的路线才有步骤；旧状态和其他路线返回空。
func NextSteps(manifest setupconfig.Manifest, route string, local LocalState, internalTLS bool) []string {
	switch route {
	case RouteFresh:
		local = LocalState{Units: map[string]bool{}}
	case RouteResume, RouteUpgrade:
		if local.StaleBinding {
			return []string{}
		}
	default:
		return []string{}
	}
	steps := []string{}
	add := func(done bool, format string, args ...any) {
		if !done {
			steps = append(steps, "./bin/moox-cli "+fmt.Sprintf(format, args...))
		}
	}
	control := manifest.ControlHost().Name
	hostsWith := func(component string) []string {
		hosts := []string{}
		for _, host := range manifest.Hosts() {
			if slices.Contains(manifest.Deployments[host.Name], component) {
				hosts = append(hosts, host.Name)
			}
		}
		return hosts
	}
	// 同一台主机上排在前面的单元先装；storage 主机上的 access 也排在 storage 之后。
	unit := func(profile, component string) {
		for _, host := range hostsWith(component) {
			add(local.Units[host+"/"+profile], "setup deploy-unit --host %s --profile %s", host, profile)
		}
	}
	add(local.CoreReady, "setup firewall --yes")
	add(local.CoreReady, "setup bootstrap --stage core")
	for _, host := range manifest.Hosts() {
		if host.Name != control {
			add(local.Units[host.Name+"/host"], "setup deploy-host --host %s", host.Name)
		}
	}
	unit("eventbus", "eventbus")
	unit("storage", "storage-primary")
	storage := manifest.ComponentHost("storage-primary")
	add(local.InitStorage, "setup init --stage storage --config-dir ./config/setup --storage-host %s", storage)
	unit("access", "access")
	unit("egress-proxy", "egress-proxy")
	if internalTLS {
		add(local.ControlReady, "setup bootstrap --stage control --create-proxy-ca")
		add(local.InitReady, "setup trust-browser")
	} else {
		add(local.ControlReady, "setup bootstrap --stage control")
	}
	add(local.InitReady, "setup init --config-dir ./config/setup --storage-host %s", storage)
	unit("collector-stream", "collector-stream")
	unit("trade", "trade")
	engines := hostsWith("factor-engine")
	if len(engines) != 0 && !local.Units[engines[0]+"/factor-engine"] {
		add(false, "setup sync-deployments --host %s --yes", engines[0])
	}
	unit("factor-engine", "factor-engine")
	unit("archive-worker", "archive-worker")
	if len(steps) != 0 {
		add(false, "config backup")
	}
	return steps
}
