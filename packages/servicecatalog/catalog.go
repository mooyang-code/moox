// Package servicecatalog owns the shared component, method and permission
// definitions. It has no database, network or deployment side effects.
package servicecatalog

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"

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
	Components []Component `yaml:"components" json:"components"`
	Principals []Principal `yaml:"principals" json:"principals"`
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
	// ExternalProgram 标记运行在登记主机上、但不经主机网关路由的外部程序（因子引擎、归档 worker）：
	// 它以同名外部身份（Principal）经 Access 访问系统，没有自己的服务与监听端口，只有一个本机健康端口。
	ExternalProgram bool `yaml:"external_program,omitempty" json:"external_program,omitempty"`
}

type Health struct {
	Kind      string `yaml:"kind" json:"kind"`
	Port      int    `yaml:"port,omitempty" json:"port,omitempty"`
	Loopback  bool   `yaml:"loopback,omitempty" json:"loopback,omitempty"`
	ReadyBody string `yaml:"ready_body,omitempty" json:"ready_body,omitempty"`
}

type Service struct {
	Path        string `yaml:"path" json:"path"`
	Port        int    `yaml:"port" json:"port"`
	ConsoleName string `yaml:"console_name,omitempty" json:"console_name,omitempty"`
	TimeoutMS   int64  `yaml:"timeout_ms,omitempty" json:"timeout_ms,omitempty"`
	// MethodTimeoutsMS 覆盖个别长等待方法的超时，未列出的方法使用 TimeoutMS。
	MethodTimeoutsMS map[string]int64 `yaml:"method_timeouts_ms,omitempty" json:"method_timeouts_ms,omitempty"`
	MaxBodyBytes     int64            `yaml:"max_body_bytes,omitempty" json:"max_body_bytes,omitempty"`
	Methods          []string         `yaml:"methods" json:"methods"`
	ReadOnlyMethods  []string         `yaml:"read_only_methods,omitempty" json:"read_only_methods,omitempty"`
	// IdempotentMethods 是写方法中重复提交同一请求不会产生额外副作用的方法：请求可能已发出的网络失败后，
	// 网关客户端可以像只读方法一样换连接重试一次。必须由服务端语义保证幂等，不得与只读方法重复声明。
	IdempotentMethods []string `yaml:"idempotent_methods,omitempty" json:"idempotent_methods,omitempty"`
	ACL               []Grant  `yaml:"acl" json:"acl"`
}

type Grant struct {
	Methods []string `yaml:"methods" json:"methods"`
	Callers []string `yaml:"callers" json:"callers"`
}

type Principal struct {
	ID    string       `yaml:"id" json:"id"`
	Allow []Permission `yaml:"allow" json:"allow"`
}

type Permission struct {
	Service string   `yaml:"service" json:"service"`
	Methods []string `yaml:"methods" json:"methods"`
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
	if timeout := s.MethodTimeoutsMS[method]; timeout > 0 {
		return timeout
	}
	if s.TimeoutMS > 0 {
		return s.TimeoutMS
	}
	return DefaultTimeoutMS
}
