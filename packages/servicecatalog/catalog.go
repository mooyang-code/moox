// Package servicecatalog owns the shared component, method and permission
// definitions. It has no database, network or deployment side effects.
package servicecatalog

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"slices"
	"sort"

	"gopkg.in/yaml.v3"
)

const (
	ScopeHost                 = "host"
	ScopeControl              = "control"
	ScopeAny                  = "any"
	Single                    = "single"
	Multi                     = "multi"
	Enabled                   = "enabled"
	Disabled                  = "disabled"
	GatewayControlPath        = "trpc.moox.admin.GatewayControl"
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
	// 它以同名外部身份（identities）经 Access 访问系统；除健康端口与可选的 RelayHTTP 端口外，还可以声明只监听本机的服务，
	// 由所在主机的网关按路由转发（因子引擎的 EngineControl），不能声明其他端口。
	ExternalProgram bool `yaml:"external_program,omitempty" json:"external_program,omitempty"`
	// RelayHTTP 是外部程序只听回环地址的 HTTP 端口：装在只出站主机上时，声明的调用方可经 control 中转连接它
	// （归档 worker 的下载与预览）。端口本身不验签，由 control 的中转按调用方白名单放行。
	RelayHTTP *RelayHTTP `yaml:"relay_http,omitempty" json:"relay_http,omitempty"`
}

// RelayHTTP 是可经中转连接的本机 HTTP 端口与允许打开它的内置调用方。
type RelayHTTP struct {
	Port    int      `yaml:"port" json:"port"`
	Callers []string `yaml:"callers" json:"callers"`
}

type Health struct {
	Kind      string `yaml:"kind" json:"kind"`
	Port      int    `yaml:"port,omitempty" json:"port,omitempty"`
	Loopback  bool   `yaml:"loopback,omitempty" json:"loopback,omitempty"`
	ReadyBody string `yaml:"ready_body,omitempty" json:"ready_body,omitempty"`
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
	// JSON 不带 omitempty：nil 与空切片要能区分着往返（Monitor 按 JSON 保存目录），否则 [] 会悄悄放宽成两者都能调。
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
	// Supervisor 是进程的托管方式：moox_runtime（默认，由 moox-runtime 看护）或 systemd_user（systemd 用户服务）。
	Supervisor string `yaml:"supervisor,omitempty" json:"supervisor,omitempty"`
	// Unit 是 systemd 用户服务的单元名，只有 systemd_user 托管的组件才有。
	Unit string `yaml:"unit,omitempty" json:"unit,omitempty"`
}

const (
	SupervisorMooxRuntime = "moox_runtime"
	SupervisorSystemdUser = "systemd_user"
)

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
