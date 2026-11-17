// Package servicecatalog owns the shared component, method and permission
// definitions. It has no database, network or deployment side effects.
package servicecatalog

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ScopeHost                 = "host"
	ScopeControl              = "control"
	ScopeAny                  = "any"
	Single                    = "single"
	Enabled                   = "enabled"
	Disabled                  = "disabled"
	GatewayControlPath        = "trpc.moox.admin.GatewayControl"
	SecretMgrPath             = "trpc.moox.ops.SecretMgr"
	DefaultTimeoutMS    int64 = 5000
	DefaultMaxBodyBytes int64 = 4 << 20
)

type Catalog struct {
	Version    int         `yaml:"version" json:"version"`
	Identities []Identity  `yaml:"identities" json:"identities"`
	Components []Component `yaml:"components" json:"components"`
}

// Identity 是不对应任何部署的调用身份。External 为真时它是外部身份：只能经 Access 进入，
// 能调的方法是 callers 里写了它的那些。
type Identity struct {
	ID       string `yaml:"id" json:"id"`
	External bool   `yaml:"external" json:"external"`
}

type Component struct {
	ID        string `yaml:"id" json:"id"`
	Name      string `yaml:"name" json:"name"`
	Binary    string `yaml:"binary" json:"binary"`
	Scope     string `yaml:"scope" json:"scope"`
	Replicas  string `yaml:"replicas" json:"replicas"`
	Protected bool   `yaml:"protected" json:"protected"`
	// Ports reserves listeners which are not forwarded RPC services, such as
	// the browser entrypoint, NATS and Storage's private runtime endpoints.
	Ports    []int     `yaml:"ports,omitempty" json:"ports,omitempty"`
	Health   Health    `yaml:"health" json:"health"`
	Services []Service `yaml:"services,omitempty" json:"services,omitempty"`
	Doctor   Doctor    `yaml:"doctor" json:"doctor"`
	// ExternalProgram 标记运行在登记主机上、由 systemd 用户服务托管的外部程序（因子引擎、归档 worker）：
	// 它和别的组件一样用所在主机的密钥、以自己的组件 ID 签名；除健康端口与可选的 RelayHTTP 端口外，还可以声明只监听本机的服务，
	// 由所在主机的网关按路由转发（因子引擎的 EngineControl），不能声明其他端口。
	ExternalProgram bool `yaml:"external_program,omitempty" json:"external_program,omitempty"`
	// RelayHTTP 是外部程序只听回环地址的 HTTP 端口：装在只出站主机上时，声明的调用方可经 control 中转连接它
	// （归档 worker 的下载与预览）。端口本身不验签，由 control 的中转按调用方白名单放行。
	RelayHTTP *RelayHTTP `yaml:"relay_http,omitempty" json:"relay_http,omitempty"`
	// ThirdParty 标记第三方程序（内置对象存储 SeaweedFS）：二进制不叫 moox-*、没有健康端口，
	// 不签名、不上报、不登记到 Admin；部署由 moox-cli 按拓扑推出来，不写在 [deployments] 里。
	ThirdParty bool `yaml:"third_party,omitempty" json:"third_party,omitempty"`

	// 以下是部署字段（moox-cli setup deploy 用）。占位 {root}、{config}、{data}、{logs}、{secrets} 在部署时
	// 换成部署根、config/<组件>、data/<组件>、logs/<组件>、secrets 的绝对路径。

	// Args 是 ExecStart 里二进制之后的参数。
	Args []string `yaml:"args" json:"args,omitempty"`
	// Tools 是随组件装进 bin/ 的配套命令行。
	Tools []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	// ConfigFiles 是从仓库复制到 config/<组件>/ 再由部署渲染的文件；部署生成的文件（主机网关的 app.yaml、存储策略）不在这里。
	ConfigFiles []ConfigFile `yaml:"config_files,omitempty" json:"config_files,omitempty"`
	// After 是要先启动的组件（systemd 的 After=）：只对同一台主机上的组件生效，不在同一台的只作说明，
	// 依赖没起来时组件自己重试或退出，由 systemd 再拉起。
	After []string `yaml:"after,omitempty" json:"after,omitempty"`
	// StopTimeoutSec 是停止的最长等待（TimeoutStopSec），省略为 DefaultStopTimeoutSec。
	StopTimeoutSec int `yaml:"stop_timeout_sec,omitempty" json:"stop_timeout_sec,omitempty"`
	// Credentials 是部署要给它的凭据：eventbus:<角色> 是 EventBus 角色文件；cls 是服务日志上报 CLS 的写入密钥。
	// 除 host-agent 与第三方程序外，每个组件还隐含一个 eventbus:metrics-publisher（上报指标）。
	Credentials []string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
}

// DefaultStopTimeoutSec 是组件停止的默认最长等待。
const DefaultStopTimeoutSec = 120

// ConfigFile 是一个要放进 config/<组件>/ 的文件。
type ConfigFile struct {
	// From 是仓库里的源文件（相对仓库根）。
	From string `yaml:"from" json:"from"`
	// To 是在 config/<组件>/ 下的相对路径，省略为 From 的文件名。
	To string `yaml:"to,omitempty" json:"to,omitempty"`
}

// Target 是这个文件在 config/<组件>/ 下的相对路径。
func (f ConfigFile) Target() string {
	if f.To != "" {
		return f.To
	}
	return path.Base(f.From)
}

// StopTimeout 是组件的停止等待秒数。
func (c Component) StopTimeout() int {
	if c.StopTimeoutSec > 0 {
		return c.StopTimeoutSec
	}
	return DefaultStopTimeoutSec
}

// EventBusRoles 是组件要的 EventBus 角色（含隐含的 metrics-publisher），已排序。
func (c Component) EventBusRoles() []string {
	roles := []string{}
	for _, credential := range c.Credentials {
		if role, ok := strings.CutPrefix(credential, "eventbus:"); ok {
			roles = append(roles, role)
		}
	}
	if !c.ThirdParty && c.ID != "host-agent" {
		roles = append(roles, "metrics-publisher")
	}
	sort.Strings(roles)
	return slices.Compact(roles)
}

// HasCredential 报告组件是否声明了某项凭据（例如 cls）。
func (c Component) HasCredential(name string) bool { return slices.Contains(c.Credentials, name) }

// RelayHTTP 是可经中转连接的本机 HTTP 端口与允许打开它的内置调用方。
type RelayHTTP struct {
	Port    int      `yaml:"port" json:"port"`
	Callers []string `yaml:"callers" json:"callers"`
}

// Health 是组件的健康端口：/healthz、/readyz、/metrics 只监听 127.0.0.1、不验签，
// 给同机进程与 moox-cli doctor 经 SSH 在目标机上请求；Monitor 不探测它（组件健康只看上报）。
type Health struct {
	Port int `yaml:"port" json:"port"`
}

type Service struct {
	Path         string   `yaml:"path" json:"path"`
	Port         int      `yaml:"port" json:"port"`
	ConsoleName  string   `yaml:"console_name,omitempty" json:"console_name,omitempty"`
	TimeoutMS    int64    `yaml:"timeout_ms,omitempty" json:"timeout_ms,omitempty"`
	MaxBodyBytes int64    `yaml:"max_body_bytes,omitempty" json:"max_body_bytes,omitempty"`
	Methods      []Method `yaml:"methods" json:"methods"`
}

// Method 是服务上的一个方法与它的全部属性；方法名在目录里只写这一次。
type Method struct {
	Name     string `yaml:"name" json:"name"`
	ReadOnly bool   `yaml:"read_only,omitempty" json:"read_only,omitempty"`
	// Idempotent 标记重复提交同一请求不会产生额外副作用的写方法：请求可能已发出的网络失败后，
	// 网关客户端可以像只读方法一样换连接重试一次。必须由服务端语义保证幂等，不能与 ReadOnly 同时声明。
	Idempotent bool `yaml:"idempotent,omitempty" json:"idempotent,omitempty"`
	// TimeoutMS 覆盖个别长等待方法的超时，为 0 时用服务级超时。
	TimeoutMS int64 `yaml:"timeout_ms,omitempty" json:"timeout_ms,omitempty"`
	// Callers 是能调它的机器身份：组件 ID、identities 里的外部身份，host-gateway 指每台主机的网关。省略表示没有机器能调。
	Callers []string `yaml:"callers,omitempty" json:"callers,omitempty"`
	// Operators 收窄人操作的两个身份（console、moox-cli）：nil 表示两者都能调，空切片表示都不能调。
	// JSON 不带 omitempty：序列化时每个方法都明确写出这个键，[] 写成 []（都不允许）、nil 写成 null（两者都能调），
	// 读 JSON（例如 Monitor 保存的目录）时一眼看出是哪一种。指针字段加 omitempty 也只省略 nil，[] 照样能往返，不加只是为了写明白。
	Operators *[]string `yaml:"operators,omitempty" json:"operators"`
}

// 人操作的两个身份：方法默认对两者开放，operators 可以收窄。
const (
	OperatorConsole = "console"
	OperatorMooxCLI = "moox-cli"
)

// OperatorList 返回能调这个方法的人操作身份（排序后的副本）。
func (m Method) OperatorList() []string {
	if m.Operators == nil {
		return []string{OperatorConsole, OperatorMooxCLI}
	}
	operators := slices.Clone(*m.Operators)
	sort.Strings(operators)
	return operators
}

// Method 按名字取方法。
func (s Service) Method(name string) (Method, bool) {
	for _, method := range s.Methods {
		if method.Name == name {
			return method, true
		}
	}
	return Method{}, false
}

// MethodNames 按目录顺序返回服务的方法名。
func (s Service) MethodNames() []string {
	names := make([]string, 0, len(s.Methods))
	for _, method := range s.Methods {
		names = append(names, method.Name)
	}
	return names
}

// Doctor keeps the diagnostics metadata in the same source of truth without
// making this package depend on Doctor or a service implementation.
type Doctor struct {
	Role                     string   `yaml:"role" json:"role"`
	Description              string   `yaml:"description" json:"description"`
	Duties                   []string `yaml:"duties" json:"duties"`
	Inputs                   []string `yaml:"inputs" json:"inputs"`
	Outputs                  []string `yaml:"outputs" json:"outputs"`
	Dependencies             []string `yaml:"dependencies" json:"dependencies"`
	Transport                string   `yaml:"transport" json:"transport"`
	FunctionalObservability  string   `yaml:"functional_observability" json:"functional_observability"`
	ConfigPaths              []string `yaml:"config_paths" json:"config_paths"`
	WritablePaths            []string `yaml:"writable_paths" json:"writable_paths"`
	RecoveryActionIDs        []string `yaml:"recovery_action_ids" json:"recovery_action_ids"`
	RequiredInDefaultProfile bool     `yaml:"required_in_default_profile" json:"required_in_default_profile"`
}

// UnitName 是组件的 systemd 用户服务名：全部组件都由 systemd 用户服务托管。
func UnitName(componentID string) string { return "moox-" + componentID + ".service" }

//go:embed catalog.yaml
var embedded []byte

func LoadEmbedded() (Catalog, error) { return Decode(bytes.NewReader(embedded)) }

// EmbeddedYAML returns an owned copy for release identity checks. Mutating it
// does not change the catalog loaded by this package.
func EmbeddedYAML() []byte { return bytes.Clone(embedded) }

func Decode(r io.Reader) (Catalog, error) {
	raw, err := io.ReadAll(io.LimitReader(r, (2<<20)+1))
	if err != nil {
		return Catalog{}, fmt.Errorf("read catalog: %w", err)
	}
	if len(raw) > 2<<20 {
		return Catalog{}, fmt.Errorf("catalog exceeds 2 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var c Catalog
	if err := decoder.Decode(&c); err != nil {
		return Catalog{}, fmt.Errorf("decode catalog: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Catalog{}, fmt.Errorf("catalog must contain one YAML document")
	}
	if err := c.Validate(); err != nil {
		return Catalog{}, err
	}
	return c, nil
}

func (c Catalog) Component(id string) (Component, bool) {
	for _, component := range c.Components {
		if component.ID == id {
			return component, true
		}
	}
	return Component{}, false
}

func (c Catalog) Service(path string) (Service, bool) {
	for _, component := range c.Components {
		for _, service := range component.Services {
			if service.Path == path {
				return service, true
			}
		}
	}
	return Service{}, false
}

// Storage exposes Metadata, PrimaryStore and DataView under one browser name.
// Resolve the pair, checking console permission before choosing the service.
func (c Catalog) ConsoleService(name, method string) (Service, bool) {
	if name == "" {
		return Service{}, false
	}
	for _, component := range c.Components {
		for _, service := range component.Services {
			if service.ConsoleName == name && c.Allowed("console", service.Path, method) {
				return service, true
			}
		}
	}
	return Service{}, false
}

// TimeoutFor 返回方法的路由超时（毫秒）：方法级覆盖优先，其次服务级，最后默认值。
func (s Service) TimeoutFor(method string) int64 {
	if m, ok := s.Method(method); ok && m.TimeoutMS > 0 {
		return m.TimeoutMS
	}
	if s.TimeoutMS > 0 {
		return s.TimeoutMS
	}
	return DefaultTimeoutMS
}
