// Package release 按 moox.toml 与组件目录渲染一台主机的发布内容：组件配置、运行规格（启动参数、环境变量、健康探测）、
// 需要的二进制和密钥。渲染不读取任何主机状态，同样的输入总是得到同样的结果。
//
// 主机上的目录布局：
//
//	<root>/releases/<版本>/   一次发布：bin/、<组件>/config/、runtime/、lib/ 和运行脚本
//	<root>/current            指向当前发布
//	<root>/data/<目录>/        数据
//	<root>/logs/<组件>/        日志
//	<root>/run/               PID 文件和 paused/<组件> 暂停标记
//	<root>/secrets/           签名密钥、共享密钥和 eventbus/ 下的消息总线凭据
//	<root>/certs/             MooX CA 与主机网关证书
package release

import (
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// Options 是渲染需要的外部输入。
type Options struct {
	// RepositoryRoot 是仓库根目录，用于读取模块的默认配置和 schema。
	RepositoryRoot string
	// Version 写入组件的 MOOX_VERSION。
	Version string
}

// Plan 是一台主机的发布内容。
type Plan struct {
	HostID string
	Root   string
	// Components 按启动顺序排列。
	Components []Component
	// Files 是发布目录中的配置、schema 和运行规格，路径相对于发布目录。
	Files []File
}

// File 是发布目录中的一个文件。
type File struct {
	Path string
	Mode fs.FileMode
	Data []byte
}

// CallerKey 是组件需要的一个调用方签名密钥：Identity 是调用方身份，File 是 <root>/secrets 下的文件名。
type CallerKey struct {
	Identity, File string
}

// Health 是组件的健康探测：readyz 带 health HMAC 请求本机端口的 /readyz（存活探测用 /healthz）；
// https 请求 URL，2xx 或 3xx 即为正常；none 不探测。
type Health struct {
	Kind string
	Port int
	URL  string
}

// Component 是一个组件在这台主机上的运行规格。
type Component struct {
	ID     string
	Binary string
	// Binaries 是放进 bin/ 的全部二进制：组件本身和启动前要用的命令行工具。
	Binaries []string
	// BuildTargets 是 scripts/build/build.sh 的构建目标；CGOTarget 非空时交叉编译要在编译主机上构建。
	BuildTargets []string
	CGOTarget    string
	// Caddy 表示二进制来自 Caddy 发布包，而不是仓库构建。
	Caddy bool
	// Workdir 是相对于发布目录的工作目录。
	Workdir string
	Args    []string
	// Env 是启动时设置的环境变量（NAME=VALUE）。
	Env []string
	// SecretEnv 是 <root>/secrets 下要导入环境变量的文件。
	SecretEnv []string
	// CallerKeys 是组件需要的调用方签名密钥。
	CallerKeys []CallerKey
	// EventBusFiles 是 <root>/secrets/eventbus 下需要的消息总线凭据（含 ca.pem）。
	EventBusFiles []string
	// SharedSecrets 是要从 control 复制过来的共享密钥文件（<root>/secrets 下）。
	SharedSecrets []string
	Health        Health
	// StartupGrace 是启动后健康检查失败也不重启的宽限时间（秒）。
	StartupGrace int
	// NeedsMetadata 表示组件启动时要读 Storage 里的元数据（空间、标签、数据集），空环境里要等 setup init 导入之后才能启动。
	NeedsMetadata bool
	// StopTimeout 是停止时等待进程退出的时间（秒），超时后强制结束。
	StopTimeout int
	// DataDirs 是 <root> 下要预先创建的数据目录。
	DataDirs []string
	// Prestart、Poststart 是启动前、就绪后在发布目录下执行的 shell 命令（已转义），可以引用 ROOT、RELEASE。
	Prestart  []string
	Poststart []string
}

// Component 按 ID 返回组件。
func (p Plan) Component(id string) (Component, bool) {
	for _, component := range p.Components {
		if component.ID == id {
			return component, true
		}
	}
	return Component{}, false
}

// ComponentIDs 返回全部组件 ID，按启动顺序。
func (p Plan) ComponentIDs() []string {
	ids := make([]string, 0, len(p.Components))
	for _, component := range p.Components {
		ids = append(ids, component.ID)
	}
	return ids
}

// startOrder 是组件在一台主机上的启动顺序：消息总线和管理后台先于主机网关（control 的主机网关直连本机的网关控制），
// 主机网关先于其他组件（它们经主机网关调用别的组件），存储按数据节点、主服务、视图的顺序，控制台代理最后。
var startOrder = []string{
	"eventbus", "admin", "host-gateway", "host-agent", "web-host", "monitor",
	"storage-node", "storage-primary", "storage-view", "access", "egress-proxy",
	"cloudnode", "collector", "factor-mgr", "strategy", "trade", "archive", "console-proxy",
}

// Render 渲染一台主机的发布内容：每台主机自动部署的主机组件，加上部署表中的业务组件。
func Render(manifest setupconfig.Manifest, hostID string, opts Options) (Plan, error) {
	host, ok := manifest.Host(hostID)
	if !ok {
		return Plan{}, fmt.Errorf("moox.toml 中没有主机 %s", hostID)
	}
	if strings.TrimSpace(opts.RepositoryRoot) == "" {
		return Plan{}, fmt.Errorf("渲染发布需要仓库根目录")
	}
	r := &renderer{manifest: manifest, host: host, catalog: servicecatalog.Default(), opts: opts}
	ids := append(r.catalog.HostComponents(), manifest.Components(hostID)...)
	order := make(map[string]int, len(startOrder))
	for i, id := range startOrder {
		order[id] = i
	}
	for _, id := range ids {
		if _, known := order[id]; !known {
			return Plan{}, fmt.Errorf("不知道怎样部署组件 %s", id)
		}
	}
	sort.SliceStable(ids, func(i, j int) bool { return order[ids[i]] < order[ids[j]] })
	plan := Plan{HostID: hostID, Root: host.Root}
	for _, id := range ids {
		component, err := r.component(id)
		if err != nil {
			return Plan{}, fmt.Errorf("渲染组件 %s: %w", id, err)
		}
		plan.Components = append(plan.Components, component)
	}
	r.addFile("runtime/components", 0o644, []byte(strings.Join(ids, "\n")+"\n"))
	r.addFile("runtime/host.env", 0o644, []byte(r.hostEnv()))
	for _, component := range plan.Components {
		spec, err := componentSpec(component)
		if err != nil {
			return Plan{}, err
		}
		r.addFile("runtime/"+component.ID+".env", 0o644, spec)
	}
	sort.Slice(r.files, func(i, j int) bool { return r.files[i].Path < r.files[j].Path })
	plan.Files = r.files
	return plan, nil
}

// renderer 保存渲染一台主机时的上下文。
type renderer struct {
	manifest setupconfig.Manifest
	host     setupconfig.Host
	catalog  *servicecatalog.Catalog
	opts     Options
	files    []File
}

func (r *renderer) isControl() bool { return r.host.ID == servicecatalog.ControlHostID }

// rootPath 返回部署根目录下的绝对路径。
func (r *renderer) rootPath(parts ...string) string {
	return filepath.Join(append([]string{r.host.Root}, parts...)...)
}

func (r *renderer) dataPath(parts ...string) string {
	return r.rootPath(append([]string{"data"}, parts...)...)
}

func (r *renderer) secretPath(name string) string { return r.rootPath("secrets", name) }

func (r *renderer) eventBusFile(name string) string { return r.rootPath("secrets", "eventbus", name) }

func (r *renderer) caFile() string { return r.rootPath("certs", "moox-ca.crt") }

// currentPath 返回当前发布中的文件路径，配置引用发布内的文件时使用。
func (r *renderer) currentPath(parts ...string) string {
	return r.rootPath(append([]string{"current"}, parts...)...)
}

// eventBusURL 是本机组件访问消息总线的地址：消息总线在本机时走回环地址，否则走它所在主机的公网地址。
func (r *renderer) eventBusURL() string {
	if r.manifest.HasComponent(r.host.ID, "eventbus") {
		scheme := "nats"
		if r.manifest.EventBus.TLSEnabled {
			scheme = "tls"
		}
		return scheme + "://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(r.manifest.EventBus.Port))
	}
	return r.manifest.EventBusURL()
}

// healthIP 是健康端口的监听地址：control 上 Monitor 走回环地址探测，其他主机要让 control 能访问到。
func (r *renderer) healthIP() string {
	if r.isControl() {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}

func (r *renderer) readRepositoryFile(rel string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(r.opts.RepositoryRoot, filepath.FromSlash(rel)))
	if err != nil {
		return nil, fmt.Errorf("读取 %s: %w", rel, err)
	}
	return raw, nil
}

func (r *renderer) addFile(path string, mode fs.FileMode, data []byte) {
	r.files = append(r.files, File{Path: path, Mode: mode, Data: data})
}

// copyFile 把仓库中的文件原样放进发布目录。
func (r *renderer) copyFile(src, dst string) error {
	raw, err := r.readRepositoryFile(src)
	if err != nil {
		return err
	}
	r.addFile(dst, 0o644, raw)
	return nil
}

// patchYAML 读取仓库中的 YAML，按 fn 改写后放进发布目录。
func (r *renderer) patchYAML(src, dst string, fn func(*yamlDoc) error) error {
	raw, err := r.readRepositoryFile(src)
	if err != nil {
		return err
	}
	doc, err := parseYAML(src, raw)
	if err != nil {
		return err
	}
	if err := fn(doc); err != nil {
		return err
	}
	out, err := doc.bytes()
	if err != nil {
		return err
	}
	r.addFile(dst, 0o644, out)
	return nil
}

// trpcConfig 改写组件的 tRPC 配置：文件日志写到 <root>/logs/<组件>；healthService 非空时按主机设置它的监听地址。
func (r *renderer) trpcConfig(src, componentID, healthService string) error {
	return r.patchYAML(src, componentID+"/config/trpc_go.yaml", func(doc *yamlDoc) error {
		doc.setLogPath(r.rootPath("logs", componentID))
		if healthService != "" {
			return doc.setServiceIP(healthService, r.healthIP())
		}
		return nil
	})
}

// gatewayClient 改写配置中的 gateway_client 段：密钥、CA 和服务目录缓存都用绝对路径。
func (r *renderer) gatewayClient(doc *yamlDoc, prefix, componentID string) error {
	for path, value := range map[string]string{
		prefix + ".key_file":  r.secretPath("caller-" + componentID + ".key"),
		prefix + ".ca_file":   r.caFile(),
		prefix + ".cache_dir": r.dataPath(componentID, "gatewayclient"),
	} {
		if err := doc.set(path, value); err != nil {
			return err
		}
	}
	return nil
}

func (r *renderer) hostEnv() string {
	lines := []string{
		"# 由 moox-cli 生成，请勿手工修改。",
		"HOST_ID=" + shellQuote(r.host.ID),
		"HOST_ROOT=" + shellQuote(r.host.Root),
		"HOST_ADDRESS=" + shellQuote(r.host.Address),
		"RELEASE_VERSION=" + shellQuote(r.opts.Version),
		"LOCAL_LOG_MAX_SIZE_MB=" + strconv.Itoa(r.manifest.LocalLogs.MaxSizeMB),
		"LOCAL_LOG_BACKUP_COUNT=" + strconv.Itoa(r.manifest.LocalLogs.BackupCount),
	}
	return strings.Join(lines, "\n") + "\n"
}

// componentSpec 生成组件的运行规格（runtime/<组件>.env），由主机上的运行脚本导入。
func componentSpec(c Component) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# 由 moox-cli 生成，请勿手工修改。\n")
	scalar := func(name, value string) { b.WriteString(name + "=" + shellQuote(value) + "\n") }
	array := func(name string, values []string) {
		quoted := make([]string, 0, len(values))
		for _, value := range values {
			quoted = append(quoted, shellQuote(value))
		}
		b.WriteString(name + "=(" + strings.Join(quoted, " ") + ")\n")
	}
	scalar("COMPONENT_BINARY", c.Binary)
	scalar("COMPONENT_WORKDIR", c.Workdir)
	array("COMPONENT_ARGS", c.Args)
	array("COMPONENT_ENV", c.Env)
	array("COMPONENT_SECRET_ENV", c.SecretEnv)
	array("COMPONENT_DATA_DIRS", c.DataDirs)
	scalar("COMPONENT_HEALTH_KIND", c.Health.Kind)
	scalar("COMPONENT_HEALTH_PORT", strconv.Itoa(c.Health.Port))
	scalar("COMPONENT_HEALTH_URL", c.Health.URL)
	scalar("COMPONENT_STARTUP_GRACE", strconv.Itoa(c.StartupGrace))
	scalar("COMPONENT_STOP_TIMEOUT", strconv.Itoa(c.StopTimeout))
	needsMetadata := "0"
	if c.NeedsMetadata {
		needsMetadata = "1"
	}
	scalar("COMPONENT_NEEDS_METADATA", needsMetadata)
	hook := func(name string, commands []string) {
		if len(commands) == 0 {
			return
		}
		b.WriteString(name + "() {\n")
		for _, command := range commands {
			b.WriteString("  " + command + "\n")
		}
		b.WriteString("}\n")
	}
	hook("component_prestart", c.Prestart)
	hook("component_poststart", c.Poststart)
	return []byte(b.String()), nil
}

// shellQuote 用单引号转义一个 shell 参数。
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// shellCommand 把参数转义拼成一条 shell 命令。参数中的 $RELEASE/（位于开头或紧跟在 = 之后）保留为发布目录的
// 变量引用，因为发布目录随版本变化，渲染时还不知道。
func shellCommand(args ...string) string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		prefix, rest, found := "", arg, false
		if strings.HasPrefix(arg, "$RELEASE/") {
			rest, found = strings.TrimPrefix(arg, "$RELEASE/"), true
		} else if i := strings.Index(arg, "=$RELEASE/"); i >= 0 {
			prefix, rest, found = arg[:i+1], arg[i+len("=$RELEASE/"):], true
		}
		if !found {
			out = append(out, shellQuote(arg))
			continue
		}
		word := `"${RELEASE}"/` + shellQuote(rest)
		if prefix != "" {
			word = shellQuote(prefix) + word
		}
		out = append(out, word)
	}
	return strings.Join(out, " ")
}

// copyTree 把仓库中的目录原样放进发布目录。
func (r *renderer) copyTree(src, dst string) error {
	base := filepath.Join(r.opts.RepositoryRoot, filepath.FromSlash(src))
	return filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取 %s: %w", path, err)
		}
		r.addFile(dst+"/"+filepath.ToSlash(rel), 0o644, raw)
		return nil
	})
}

// renderCollectorRuntime 用 moox.toml 渲染 Collector app.yaml 中由部署决定的段落。
func renderCollectorRuntime(manifest setupconfig.Manifest, raw []byte) ([]byte, error) {
	return setupconfig.RenderCollectorRuntimeConfig(&setupconfig.Snapshot{Manifest: manifest}, raw)
}
