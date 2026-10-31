package command

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// lifecycle、system、replay、import 要访问模块的健康端口（回放、生命周期、本地写入接口都挂在上面，签名鉴权）。
// 以前要先手工建 SSH 隧道、再把健康密钥放进环境变量；现在由 moox.toml 解析：
// 模块在哪台主机、端口是多少（服务目录）、怎么连（直连经 SSH 转发，只能出站的主机经 control 中转）、
// 密钥从主机上的运行时文件经 SSH 读出，只放进本进程的环境变量，不打印、不落盘。
// 显式给了地址或密钥环境变量时以用户给的为准。

// healthModule 是带健康端口的模块。
type healthModule struct {
	Name      string // 命令行里的模块名
	Component string // 服务目录里的组件 ID
	EnvPrefix string // 环境变量 MOOX_<前缀>_HEALTH_AUTH_* 里的前缀
}

var healthModules = []healthModule{
	{"collector", "collector", "COLLECTOR"},
	{"factor-engine", "factor-engine", "FACTOR"},
	{"strategy", "strategy", "STRATEGY"},
	{"trade", "trade", "TRADE"},
	{"factor-mgr", "factor-mgr", "FACTORMGR"},
	{"archive-mgr", "archive-mgr", "ARCHIVE"},
	{"monitor", "monitor", "MONITOR"},
}

func healthModuleByName(name string) (healthModule, bool) {
	for _, module := range healthModules {
		if module.Name == name {
			return module, true
		}
	}
	return healthModule{}, false
}

// errManifestUnavailable 表示没有 moox.toml（例如直接在模块所在的主机上运行），调用方退回“显式地址 + 环境变量”的用法。
var errManifestUnavailable = errors.New("没有可用的 moox.toml")

// healthSecretSource 是主机上可能存放健康密钥的一个位置。
type healthSecretSource struct {
	Argv   []string
	Format string // json：运行时环境文件（{"MOOX_…": "…"}）；env：KEY=VALUE 行
	Label  string
}

// healthSecretSources 按优先级列出模块的健康密钥文件：
// 控制机上的组件在 <control_root>/current/secrets/runtime-<组件>.json，其他业务单元在 <deploy_root>/<单元>/current/secrets/ 下，
// 外部程序（因子引擎、归档 worker）在 <deploy_root>/<程序>/secrets/health-auth.env，也可能装在登录用户的 ~/moox 下。
func healthSecretSources(manifest setupconfig.Manifest, component string) []healthSecretSource {
	paths := manifest.Paths.Resolved()
	cat := func(p string) []string { return []string{"cat", p} }
	return []healthSecretSource{
		{cat(path.Join(paths.ControlRoot, "current", "secrets", "runtime-"+component+".json")), "json", path.Join(paths.ControlRoot, "current/secrets/runtime-"+component+".json")},
		{cat(path.Join(paths.DeployRoot, component, "current", "secrets", "runtime-"+component+".json")), "json", path.Join(paths.DeployRoot, component, "current/secrets/runtime-"+component+".json")},
		{cat(path.Join(paths.DeployRoot, component, "secrets", "health-auth.env")), "env", path.Join(paths.DeployRoot, component, "secrets/health-auth.env")},
		{[]string{"sh", "-c", `cat "$HOME/moox/` + component + `/secrets/health-auth.env"`}, "env", "~/moox/" + component + "/secrets/health-auth.env"},
	}
}

// parseHealthSecrets 解析密钥文件内容为 MOOX_ 变量表。
func parseHealthSecrets(raw, format string) (map[string]string, error) {
	values := map[string]string{}
	switch format {
	case "json":
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, errors.New("运行时环境文件不是合法的 JSON")
		}
	default:
		scanner := bufio.NewScanner(strings.NewReader(raw))
		for scanner.Scan() {
			line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "export "))
			key, value, ok := strings.Cut(line, "=")
			if !ok || strings.HasPrefix(line, "#") {
				continue
			}
			values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return values, nil
}

// moduleHealth 是一次解析的结果：每个模块在本机回环地址上的健康端点。
type moduleHealth struct {
	remote    *hostRemote
	gateway   io.Closer
	endpoints map[string]string
	// NotDeployed 是 moox.toml 里没有部署的模块。
	NotDeployed []string
}

// Endpoint 返回模块的本机端点（http://127.0.0.1:端口）；没有部署返回空。
func (h *moduleHealth) Endpoint(name string) string { return h.endpoints[name] }

func (h *moduleHealth) Close() {
	if h == nil {
		return
	}
	h.remote.Close()
	if h.gateway != nil {
		_ = h.gateway.Close()
	}
}

type moduleHealthRequest struct {
	File    string
	Modules []string
	// GenericFrom 非空时，该模块的密钥同时设为通用的 MOOX_HEALTH_AUTH_*（lifecycle 与 import 只读通用变量）。
	GenericFrom string
}

// resolveModuleHealth 按 moox.toml 解析模块的健康端点并装好密钥环境变量；测试里替换。
var resolveModuleHealth = func(ctx context.Context, request moduleHealthRequest) (*moduleHealth, error) {
	// setupconfig.Load 读不到文件时的错误没有包装 os.ErrNotExist，所以先自己判断文件在不在：
	// 直接在模块所在的主机上运行时没有 moox.toml，要退回本机地址而不是报错。
	if absolute, err := filepath.Abs(defaultFlag(request.File, defaultSetupFile)); err == nil {
		if _, statErr := os.Stat(absolute); errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", errManifestUnavailable, absolute)
		}
	}
	remote, err := openHostRemote(request.File, "", nil)
	if err != nil {
		return nil, err
	}
	return resolveModuleHealthWith(ctx, remote, request)
}

// resolveModuleHealthWith 用已装配好的连接层解析各模块的健康端点；出错时释放 remote。
func resolveModuleHealthWith(ctx context.Context, remote *hostRemote, request moduleHealthRequest) (*moduleHealth, error) {
	health := &moduleHealth{remote: remote, endpoints: map[string]string{}}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		health.Close()
		return nil, err
	}
	outbound := false
	type target struct {
		module healthModule
		host   string
		port   int
	}
	var targets []target
	for _, name := range request.Modules {
		module, ok := healthModuleByName(name)
		if !ok {
			health.Close()
			return nil, fmt.Errorf("未知的模块 %q", name)
		}
		component, ok := catalog.Component(module.Component)
		if !ok || component.Health.Port == 0 {
			health.Close()
			return nil, fmt.Errorf("服务目录里没有 %s 的健康端口", module.Component)
		}
		host := remote.manifest.ComponentHost(module.Component)
		if host == "" {
			if hosts := componentHosts(remote.manifest, module.Component); len(hosts) > 1 {
				health.Close()
				return nil, fmt.Errorf("%s 部署在多台主机上（%s），不能自动选择，请用 --endpoint（或该命令的地址参数）显式指定", name, strings.Join(hosts, "、"))
			}
			health.NotDeployed = append(health.NotDeployed, name)
			continue
		}
		outbound = outbound || remote.outbound(host)
		targets = append(targets, target{module, host, component.Health.Port})
	}
	if outbound {
		// 只能出站的主机经 control 中转，需要操作员网关。
		gateway, err := openCommandGateway(ctx, request.File, nil)
		if err != nil {
			health.Close()
			return nil, err
		}
		health.gateway = gateway
		if relay, ok := any(gateway).(relayDialer); ok {
			remote.relay = relay
		}
	}
	for _, t := range targets {
		// 用户已经给了密钥（模块自己的或通用的环境变量）就以用户的为准，不再读主机上的文件。
		if os.Getenv("MOOX_"+t.module.EnvPrefix+"_HEALTH_AUTH_SECRET_KEY") == "" && os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY") == "" {
			secrets, err := remote.readHealthSecrets(ctx, t.host, t.module.Component)
			if err != nil {
				health.Close()
				return nil, fmt.Errorf("读取 %s 的健康密钥失败（主机 %s）：%w", t.module.Name, t.host, err)
			}
			applyHealthSecrets(t.module, secrets, request.GenericFrom == t.module.Name)
		}
		address, err := remote.LocalEndpoint(ctx, t.host, t.port)
		if err != nil {
			health.Close()
			return nil, fmt.Errorf("连接 %s（主机 %s 端口 %d）失败：%w", t.module.Name, t.host, t.port, err)
		}
		health.endpoints[t.module.Name] = "http://" + address
	}
	return health, nil
}

// healthSecretFallbacks 是控制机上可借用的运行时环境文件：整个集群共用同一把健康密钥（部署时写进每个单元），
// 所以只能出站、没法 SSH 的主机（例如局域网里的因子机），密钥可以从控制机上的文件读到。
// 模块各自的重置专用密钥不在这里，只能从模块自己的主机读，或由用户用环境变量给出。
var healthSecretFallbacks = []string{"monitor", "collector", "strategy", "admin"}

// componentHosts 列出部署了某个组件的全部主机（已排序）。
func componentHosts(manifest setupconfig.Manifest, component string) []string {
	var hosts []string
	for host, components := range manifest.Deployments {
		if slices.Contains(components, component) {
			hosts = append(hosts, host)
		}
	}
	slices.Sort(hosts)
	return hosts
}

// readHealthSecrets 经 SSH 读取模块的健康密钥：先试模块所在主机（只能出站的主机跳过），再借用控制机上的文件；
// 第一个能解析出密钥的为准。
func (r *hostRemote) readHealthSecrets(ctx context.Context, hostID, component string) (map[string]string, error) {
	type attempt struct {
		host   string
		source healthSecretSource
	}
	var attempts []attempt
	if !r.outbound(hostID) {
		for _, source := range healthSecretSources(r.manifest, component) {
			attempts = append(attempts, attempt{hostID, source})
		}
	}
	if control := r.manifest.ControlHost().Name; control != "" && !r.outbound(control) {
		controlRoot := r.manifest.Paths.Resolved().ControlRoot
		for _, name := range healthSecretFallbacks {
			file := path.Join(controlRoot, "current", "secrets", "runtime-"+name+".json")
			attempts = append(attempts, attempt{control, healthSecretSource{Argv: []string{"cat", file}, Format: "json", Label: control + ":" + file}})
		}
	}
	var tried []string
	for _, a := range attempts {
		result, err := r.Exec(ctx, a.host, a.source.Argv, nil)
		if err != nil {
			tried = append(tried, a.source.Label)
			continue
		}
		values, err := parseHealthSecrets(result.Stdout, a.source.Format)
		if err != nil || values["MOOX_HEALTH_AUTH_SECRET_KEY"] == "" {
			tried = append(tried, a.source.Label)
			continue
		}
		return values, nil
	}
	return nil, fmt.Errorf("找不到健康密钥文件（已尝试 %s）；可以设置环境变量 MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY，或用 --endpoint 指定地址", strings.Join(tried, "、"))
}

// applyHealthSecrets 把模块的密钥放进本进程的环境变量；用户已经设置的不覆盖。
func applyHealthSecrets(module healthModule, values map[string]string, generic bool) {
	set := func(name, value string) {
		if value != "" && os.Getenv(name) == "" {
			_ = os.Setenv(name, value)
		}
	}
	version := values["MOOX_HEALTH_AUTH_VERSION"]
	if version == "" {
		version = "moox-health-v1"
	}
	for _, name := range []string{"VERSION", "ACCESS_KEY", "SECRET_KEY"} {
		value := values["MOOX_HEALTH_AUTH_"+name]
		if name == "VERSION" {
			value = version
		}
		set("MOOX_"+module.EnvPrefix+"_HEALTH_AUTH_"+name, value)
		if generic {
			set("MOOX_HEALTH_AUTH_"+name, value)
		}
	}
	for _, name := range []string{"ACCESS_KEY", "SECRET_KEY"} {
		set("MOOX_"+module.EnvPrefix+"_HEALTH_RESET_"+name, values["MOOX_HEALTH_RESET_"+name])
	}
}

// ---- 命令接入 ----

var (
	trackedHealthMu sync.Mutex
	trackedHealth   []*moduleHealth
)

// resolveAndTrackModuleHealth 解析并登记结果；命令结束时由 closeModuleHealth 统一释放连接。
func resolveAndTrackModuleHealth(ctx context.Context, request moduleHealthRequest) (*moduleHealth, error) {
	health, err := resolveModuleHealth(ctx, request)
	if err != nil {
		return nil, err
	}
	trackedHealthMu.Lock()
	trackedHealth = append(trackedHealth, health)
	trackedHealthMu.Unlock()
	return health, nil
}

// closeModuleHealth 释放本次命令里解析出的全部连接与本机端口。
func closeModuleHealth() {
	trackedHealthMu.Lock()
	defer trackedHealthMu.Unlock()
	for _, health := range trackedHealth {
		health.Close()
	}
	trackedHealth = nil
}

// defaultHealthEndpoint 是没有 moox.toml 时的回环默认地址：直接在模块所在的主机上运行命令。
func defaultHealthEndpoint(module string) string {
	item, ok := healthModuleByName(module)
	if !ok {
		return ""
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return ""
	}
	component, ok := catalog.Component(item.Component)
	if !ok || component.Health.Port == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", component.Health.Port)
}

// resolveSingleModuleEndpoint 解析一个模块的健康端点：没有 moox.toml 时退回回环默认地址，模块没有部署时报错。
func resolveSingleModuleEndpoint(ctx context.Context, file, module string, generic bool, warn io.Writer) (string, error) {
	request := moduleHealthRequest{File: file, Modules: []string{module}}
	if generic {
		request.GenericFrom = module
	}
	health, err := resolveAndTrackModuleHealth(ctx, request)
	if errors.Is(err, errManifestUnavailable) {
		if warn != nil {
			fmt.Fprintf(warn, "没有找到 %s，使用本机地址 %s；密钥取自环境变量 MOOX_HEALTH_AUTH_*\n", file, defaultHealthEndpoint(module))
		}
		return defaultHealthEndpoint(module), nil
	}
	if err != nil {
		return "", err
	}
	if endpoint := health.Endpoint(module); endpoint != "" {
		return endpoint, nil
	}
	return "", fmt.Errorf("moox.toml 里没有部署 %s", module)
}
