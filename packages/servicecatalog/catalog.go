// Package servicecatalog 是 MooX 的组件目录：组件、端口、tRPC 服务、ACL、健康检查和外部调用方白名单。
//
// 目录写在代码里（内嵌 catalog.yaml），数据库只记录「哪个组件部署在哪台主机、是否启用」。
// 管理后台、监控、CLI、主机网关和外部接入内嵌同一份目录，因此必须同版本发布。
package servicecatalog

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ControlHostID 是 control 主机的固定 ID：「control」范围的组件只能部署在这里，它本身也受保护。
const ControlHostID = "control"

// HostGatewayCaller 是主机网关的调用方身份前缀，完整身份为 host-gateway@<主机 ID>。
const HostGatewayCaller = "host-gateway"

// AccessCaller 是外部接入向主机网关转发外部调用方请求时使用的调用方身份。
const AccessCaller = "access"

// ConsoleCaller 是控制台转发浏览器请求时使用的调用方身份。
const ConsoleCaller = "console"

// Scope 是组件的部署范围。
type Scope string

const (
	// ScopeHost 表示每台主机自动部署一份，不能编辑。
	ScopeHost Scope = "host"
	// ScopeControl 表示只能部署在 control 主机。
	ScopeControl Scope = "control"
	// ScopeAny 表示可以部署到任意主机。
	ScopeAny Scope = "any"
)

// Replicas 是所有主机加起来最多允许几条启用的部署。
type Replicas string

const (
	// ReplicasPerHost 只用于「主机」范围的组件：每台主机一份。
	ReplicasPerHost Replicas = "per_host"
	// ReplicasSingle 表示全局最多一条启用的部署。
	ReplicasSingle Replicas = "single"
	// ReplicasMulti 表示允许多条启用的部署。
	ReplicasMulti Replicas = "multi"
)

// HealthKind 是监控对组件的探测方式。
type HealthKind string

const (
	// HealthReadyz 表示带 health HMAC 请求健康端口的 /readyz。
	HealthReadyz HealthKind = "readyz"
	// HealthHTTPS 表示请求 https://<主机>:<端口>/，2xx 或 3xx 即视为正常。
	HealthHTTPS HealthKind = "https"
	// HealthNone 表示不探测，界面显示「不探测」。
	HealthNone HealthKind = "none"
)

// Catalog 是解析并校验过的组件目录。字段只读，调用方不要修改。
type Catalog struct {
	Version    int         `yaml:"version"`
	Callers    []Caller    `yaml:"callers"`
	Components []Component `yaml:"components"`
	Principals []Principal `yaml:"principals"`

	checksum     string
	components   map[string]*Component
	services     map[string]*Service
	serviceOwner map[string]string
	// acl 为每个 service path 下每个方法展开后的调用方集合（已排序）。
	acl map[string]map[string][]string
	// consoleRoutes 为每个 console_name 下每个放行 console 的方法所属的 service path。
	consoleRoutes map[string]map[string]string
	principals    map[string]map[string]map[string]bool
}

// Caller 是组件 ID 之外的调用方身份，例如 console、moox-cli。
type Caller struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
}

// Component 是一种 MooX 进程。
type Component struct {
	ID        string    `yaml:"id"`
	Name      string    `yaml:"name"`
	Binary    string    `yaml:"binary"`
	Scope     Scope     `yaml:"scope"`
	Replicas  Replicas  `yaml:"replicas"`
	Protected bool      `yaml:"protected"`
	Health    Health    `yaml:"health"`
	Ports     []Port    `yaml:"ports"`
	Services  []Service `yaml:"services"`
}

// Health 是组件的健康探测定义。
type Health struct {
	Kind HealthKind `yaml:"kind"`
	Port int        `yaml:"port"`
}

// Port 是组件监听、但不经主机网关路由的端口，只用于端口冲突校验和展示。
type Port struct {
	Name string `yaml:"name"`
	Port int    `yaml:"port"`
}

// Service 是组件对外提供、经主机网关路由的一个 tRPC 服务。
type Service struct {
	Path string `yaml:"path"`
	Port int    `yaml:"port"`
	// ConsoleName 是浏览器侧的服务名（/api/admin/<console_name>/<方法>）；为空表示浏览器不可调用。
	ConsoleName  string    `yaml:"console_name"`
	TimeoutMS    int64     `yaml:"timeout_ms"`
	MaxBodyBytes int64     `yaml:"max_body_bytes"`
	RPCs         []string  `yaml:"rpcs"`
	ReadOnly     []string  `yaml:"read_only"`
	ACL          []ACLRule `yaml:"acl"`
}

// ACLRule 放行一组方法给一组调用方。methods 与 all 二选一；except 只能与 all 一起使用。
type ACLRule struct {
	Methods []string `yaml:"methods"`
	All     bool     `yaml:"all"`
	Except  []string `yaml:"except"`
	Callers []string `yaml:"callers"`
}

// Principal 是外部调用方（SCF 采集函数、因子引擎、moox-skill），只能经外部接入访问白名单里的方法。
type Principal struct {
	ID          string           `yaml:"id"`
	Description string           `yaml:"description"`
	Allow       []PrincipalGrant `yaml:"allow"`
}

// PrincipalGrant 是外部调用方在一个服务上被允许的方法。
type PrincipalGrant struct {
	Service string   `yaml:"service"`
	Methods []string `yaml:"methods"`
}

//go:embed catalog.yaml
var embeddedCatalog []byte

var (
	defaultOnce    sync.Once
	defaultCatalog *Catalog
	defaultErr     error
)

// Default 返回内嵌的组件目录。目录在单测中校验，运行时解析失败说明构建产物损坏。
func Default() *Catalog {
	defaultOnce.Do(func() {
		defaultCatalog, defaultErr = Parse(embeddedCatalog)
	})
	if defaultErr != nil {
		panic(fmt.Sprintf("内嵌组件目录无效: %v", defaultErr))
	}
	return defaultCatalog
}

// Embedded 返回内嵌 catalog.yaml 的原始字节，供发布包拷贝和校验和比对使用。
func Embedded() []byte { return append([]byte(nil), embeddedCatalog...) }

// Parse 解析并校验一份组件目录。
func Parse(raw []byte) (*Catalog, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("解析组件目录: %w", err)
	}
	sum := sha256.Sum256(raw)
	catalog.checksum = "sha256:" + hex.EncodeToString(sum[:])
	if err := catalog.build(); err != nil {
		return nil, err
	}
	return &catalog, nil
}

// Checksum 返回目录原始字节的 sha256 校验和。
func (c *Catalog) Checksum() string { return c.checksum }

// Component 按 ID 查找组件。
func (c *Catalog) Component(id string) (*Component, bool) {
	component, ok := c.components[id]
	return component, ok
}

// Service 按 tRPC service path 查找服务及其所属组件。
func (c *Catalog) Service(path string) (*Service, *Component, bool) {
	service, ok := c.services[path]
	if !ok {
		return nil, nil, false
	}
	return service, c.components[c.serviceOwner[path]], true
}

// HasRPC 判断服务是否声明了这个方法。
func (c *Catalog) HasRPC(servicePath, method string) bool {
	_, ok := c.acl[servicePath][method]
	return ok
}

// IsReadOnly 判断方法是否为幂等读：只有这类方法允许失败后重试。
func (s *Service) IsReadOnly(method string) bool {
	for _, name := range s.ReadOnly {
		if name == method {
			return true
		}
	}
	return false
}

// IsReadOnly 判断某个服务的方法是否为幂等读。
func (c *Catalog) IsReadOnly(servicePath, method string) bool {
	service, ok := c.services[servicePath]
	return ok && service.IsReadOnly(method)
}

// HasRPC 判断服务是否声明了这个方法。
func (s *Service) HasRPC(method string) bool {
	for _, name := range s.RPCs {
		if name == method {
			return true
		}
	}
	return false
}

// ConsoleTarget 把浏览器侧的 /api/admin/<console_name>/<方法> 映射到 tRPC 服务。
// 只返回放行 console 的方法。
func (c *Catalog) ConsoleTarget(consoleName, method string) (string, bool) {
	path, ok := c.consoleRoutes[consoleName][method]
	return path, ok
}

// ConsoleNames 返回全部浏览器侧服务名，已排序。
func (c *Catalog) ConsoleNames() []string {
	names := make([]string, 0, len(c.consoleRoutes))
	for name := range c.consoleRoutes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Principal 按 ID 查找外部调用方。
func (c *Catalog) Principal(id string) (*Principal, bool) {
	for i := range c.Principals {
		if c.Principals[i].ID == id {
			return &c.Principals[i], true
		}
	}
	return nil, false
}

// PrincipalAllowed 判断外部调用方是否被允许调用这个方法。外部接入和路由编译共用这一份白名单。
func (c *Catalog) PrincipalAllowed(principal, servicePath, method string) bool {
	return c.principals[principal][servicePath][method]
}

// MethodCallers 返回某个方法展开后的调用方（已排序，host-gateway 保持为前缀形式）。
func (c *Catalog) MethodCallers(servicePath, method string) []string {
	return append([]string(nil), c.acl[servicePath][method]...)
}

// HostGatewayIdentity 返回某台主机的主机网关调用方身份。
func HostGatewayIdentity(hostID string) string { return HostGatewayCaller + "@" + hostID }

// HostOfGatewayIdentity 从 host-gateway@<主机> 中取出主机 ID。
func HostOfGatewayIdentity(caller string) (string, bool) {
	host, ok := strings.CutPrefix(caller, HostGatewayCaller+"@")
	if !ok || !identifierPattern.MatchString(host) {
		return "", false
	}
	return host, true
}
