package servicecatalog

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
)

const (
	maxTimeoutMS int64 = 960000
	// maxMaxBodyBytes 必须小于 tRPC 的单帧上限（gatewayclient 提到 40 MiB），否则声明的请求体到不了服务。
	maxMaxBodyBytes int64 = 32 << 20
)

var (
	identifierPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	servicePathPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
	methodPattern      = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
)

// build 校验目录并建立索引。规则见设计文档 3.8「目录校验」。
func (c *Catalog) build() error {
	if c.Version != 1 {
		return fmt.Errorf("组件目录版本 %d 不受支持", c.Version)
	}
	if len(c.Components) == 0 {
		return errors.New("组件目录没有任何组件")
	}
	identities := map[string]string{}
	claim := func(id, kind string) error {
		if !identifierPattern.MatchString(id) {
			return fmt.Errorf("%s ID %q 只能包含小写字母、数字和连字符，且以字母开头", kind, id)
		}
		if previous, exists := identities[id]; exists {
			return fmt.Errorf("%s ID %q 与%s重复", kind, id, previous)
		}
		identities[id] = kind
		return nil
	}
	for _, caller := range c.Callers {
		if err := claim(caller.ID, "调用方"); err != nil {
			return err
		}
	}
	c.components = make(map[string]*Component, len(c.Components))
	for i := range c.Components {
		component := &c.Components[i]
		if err := claim(component.ID, "组件"); err != nil {
			return err
		}
		if err := component.validate(); err != nil {
			return fmt.Errorf("组件 %s: %w", component.ID, err)
		}
		c.components[component.ID] = component
	}
	if _, reserved := identities[HostGatewayCaller]; !reserved {
		return fmt.Errorf("组件目录缺少组件 %s", HostGatewayCaller)
	}
	if _, reserved := identities[AccessCaller]; !reserved {
		return fmt.Errorf("组件目录缺少组件 %s", AccessCaller)
	}
	if kind := identities[ConsoleCaller]; kind != "调用方" {
		return fmt.Errorf("组件目录缺少调用方 %s", ConsoleCaller)
	}
	if err := c.validatePorts(); err != nil {
		return err
	}
	if err := c.buildServices(identities); err != nil {
		return err
	}
	for i := range c.Principals {
		if err := claim(c.Principals[i].ID, "外部调用方"); err != nil {
			return err
		}
	}
	if err := c.buildPrincipals(); err != nil {
		return err
	}
	return c.buildConsoleRoutes()
}

func (component *Component) validate() error {
	if component.Name == "" {
		return errors.New("缺少名称")
	}
	if component.Binary == "" {
		return errors.New("缺少二进制名")
	}
	switch component.Scope {
	case ScopeHost:
		if component.Replicas != ReplicasPerHost {
			return fmt.Errorf("「主机」范围的组件副本数必须为 %s", ReplicasPerHost)
		}
	case ScopeControl, ScopeAny:
		if component.Replicas != ReplicasSingle && component.Replicas != ReplicasMulti {
			return fmt.Errorf("副本数必须为 %s 或 %s", ReplicasSingle, ReplicasMulti)
		}
	default:
		return fmt.Errorf("部署范围 %q 无效", component.Scope)
	}
	switch component.Health.Kind {
	case HealthReadyz, HealthHTTPS:
		if !validPort(component.Health.Port) {
			return fmt.Errorf("探测方式 %s 必须给出健康端口", component.Health.Kind)
		}
	case HealthNone:
		if component.Health.Port != 0 {
			return errors.New("不探测的组件不能声明健康端口")
		}
	default:
		return fmt.Errorf("探测方式 %q 无效", component.Health.Kind)
	}
	switch component.Observability.Transport {
	case TransportReporter, TransportHostSnapshot, TransportHealthOnly:
	default:
		return fmt.Errorf("观测方式 %q 无效", component.Observability.Transport)
	}
	switch component.Observability.Functional {
	case FunctionalActive, FunctionalDeferred:
		// 业务进度经指标上报，不上报指标的组件没有业务进度。
		if component.Observability.Transport != TransportReporter {
			return fmt.Errorf("观测方式为 %s 的组件不能声明业务进度 %s", component.Observability.Transport, component.Observability.Functional)
		}
	case FunctionalNotApplicable:
	default:
		return fmt.Errorf("业务进度 %q 无效", component.Observability.Functional)
	}
	for _, port := range component.Ports {
		if port.Name == "" || !validPort(port.Port) {
			return fmt.Errorf("端口 %q:%d 无效", port.Name, port.Port)
		}
	}
	return nil
}

func validPort(port int) bool { return port > 0 && port <= 65535 }

// validatePorts 保证任意两个组件放到同一台主机上都不会端口冲突。
func (c *Catalog) validatePorts() error {
	owners := map[int]string{}
	use := func(port int, owner string) error {
		if previous, exists := owners[port]; exists {
			return fmt.Errorf("端口 %d 同时被 %s 和 %s 使用", port, previous, owner)
		}
		owners[port] = owner
		return nil
	}
	for _, component := range c.Components {
		if component.Health.Port != 0 {
			if err := use(component.Health.Port, component.ID+" 的健康端口"); err != nil {
				return err
			}
		}
		for _, port := range component.Ports {
			if err := use(port.Port, component.ID+" 的 "+port.Name); err != nil {
				return err
			}
		}
		for _, service := range component.Services {
			if err := use(service.Port, service.Path); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Catalog) buildServices(identities map[string]string) error {
	c.services = map[string]*Service{}
	c.serviceOwner = map[string]string{}
	c.acl = map[string]map[string][]string{}
	for i := range c.Components {
		component := &c.Components[i]
		for j := range component.Services {
			service := &component.Services[j]
			if !servicePathPattern.MatchString(service.Path) {
				return fmt.Errorf("服务 %q 不是合法的 tRPC service path", service.Path)
			}
			if _, exists := c.services[service.Path]; exists {
				return fmt.Errorf("服务 %s 重复声明", service.Path)
			}
			if err := service.validate(identities); err != nil {
				return fmt.Errorf("服务 %s: %w", service.Path, err)
			}
			// 主机网关的调用方身份（host-gateway@<主机>）只用来拉取快照、上报状态：只有 control 上的网关控制可以放行它。
			// 放在别的服务的 ACL 里，会让那台主机收到所有主机网关的校验密钥，进而冒充其他主机的网关拉取快照。
			if component.Scope != ScopeControl && serviceAllowsCaller(service, HostGatewayCaller) {
				return fmt.Errorf("服务 %s: %s 只能出现在 control 范围组件的 ACL 里", service.Path, HostGatewayCaller)
			}
			c.services[service.Path] = service
			c.serviceOwner[service.Path] = component.ID
			c.acl[service.Path] = service.expandACL()
		}
	}
	return nil
}

func serviceAllowsCaller(service *Service, caller string) bool {
	for _, rule := range service.ACL {
		for _, name := range rule.Callers {
			if name == caller {
				return true
			}
		}
	}
	return false
}

func (service *Service) validate(identities map[string]string) error {
	if !validPort(service.Port) {
		return fmt.Errorf("端口 %d 无效", service.Port)
	}
	if service.TimeoutMS < 0 || service.TimeoutMS > maxTimeoutMS {
		return fmt.Errorf("timeout_ms 必须在 1～%d 之间，0 表示默认值", maxTimeoutMS)
	}
	if service.MaxBodyBytes < 0 || service.MaxBodyBytes > maxMaxBodyBytes {
		return fmt.Errorf("max_body_bytes 必须在 1～%d 之间，0 表示默认值", maxMaxBodyBytes)
	}
	if service.ConsoleName != "" && !identifierPattern.MatchString(service.ConsoleName) {
		return fmt.Errorf("console_name %q 无效", service.ConsoleName)
	}
	if len(service.RPCs) == 0 {
		return errors.New("没有声明任何方法")
	}
	declared := map[string]bool{}
	for _, method := range service.RPCs {
		if !methodPattern.MatchString(method) {
			return fmt.Errorf("方法名 %q 无效", method)
		}
		if declared[method] {
			return fmt.Errorf("方法 %s 重复声明", method)
		}
		declared[method] = true
	}
	if err := subset(service.ReadOnly, declared, "read_only"); err != nil {
		return err
	}
	for index, rule := range service.ACL {
		if err := rule.validate(declared, identities); err != nil {
			return fmt.Errorf("第 %d 条 ACL: %w", index+1, err)
		}
	}
	return nil
}

func (rule ACLRule) validate(declared map[string]bool, identities map[string]string) error {
	switch {
	case rule.All && len(rule.Methods) > 0:
		return errors.New("methods 与 all 只能二选一")
	case !rule.All && len(rule.Methods) == 0:
		return errors.New("必须给出 methods 或 all")
	case !rule.All && len(rule.Except) > 0:
		return errors.New("except 只能与 all 一起使用")
	}
	if err := subset(rule.Methods, declared, "methods"); err != nil {
		return err
	}
	if err := subset(rule.Except, declared, "except"); err != nil {
		return err
	}
	if len(rule.Callers) == 0 {
		return errors.New("没有调用方")
	}
	seen := map[string]bool{}
	for _, caller := range rule.Callers {
		if seen[caller] {
			return fmt.Errorf("调用方 %s 重复", caller)
		}
		seen[caller] = true
		if kind, ok := identities[caller]; !ok || kind == "外部调用方" {
			return fmt.Errorf("调用方 %q 未定义", caller)
		}
	}
	return nil
}

func subset(values []string, declared map[string]bool, field string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if !declared[value] {
			return fmt.Errorf("%s 引用了未声明的方法 %q", field, value)
		}
		if seen[value] {
			return fmt.Errorf("%s 中方法 %s 重复", field, value)
		}
		seen[value] = true
	}
	return nil
}

// expandACL 把规则展开成「方法 → 调用方集合」。没有任何规则放行的方法不经路由，例如 Storage 的内部方法。
func (service *Service) expandACL() map[string][]string {
	sets := make(map[string]map[string]bool, len(service.RPCs))
	for _, method := range service.RPCs {
		sets[method] = map[string]bool{}
	}
	for _, rule := range service.ACL {
		methods := rule.Methods
		if rule.All {
			excluded := map[string]bool{}
			for _, method := range rule.Except {
				excluded[method] = true
			}
			methods = nil
			for _, method := range service.RPCs {
				if !excluded[method] {
					methods = append(methods, method)
				}
			}
		}
		for _, method := range methods {
			for _, caller := range rule.Callers {
				sets[method][caller] = true
			}
		}
	}
	out := make(map[string][]string, len(sets))
	for method, set := range sets {
		out[method] = sortedKeys(set)
	}
	return out
}

func (c *Catalog) buildPrincipals() error {
	c.principals = map[string]map[string]map[string]bool{}
	for _, principal := range c.Principals {
		if len(principal.Allow) == 0 {
			return fmt.Errorf("外部调用方 %s 没有任何可调用的方法", principal.ID)
		}
		grants := map[string]map[string]bool{}
		for _, grant := range principal.Allow {
			service, ok := c.services[grant.Service]
			if !ok {
				return fmt.Errorf("外部调用方 %s 引用了未声明的服务 %q", principal.ID, grant.Service)
			}
			if _, duplicated := grants[grant.Service]; duplicated {
				return fmt.Errorf("外部调用方 %s 重复声明服务 %s", principal.ID, grant.Service)
			}
			if len(grant.Methods) == 0 {
				return fmt.Errorf("外部调用方 %s 在 %s 上没有方法", principal.ID, grant.Service)
			}
			declared := map[string]bool{}
			for _, method := range service.RPCs {
				declared[method] = true
			}
			if err := subset(grant.Methods, declared, "外部调用方 "+principal.ID+" 的 methods"); err != nil {
				return err
			}
			methods := map[string]bool{}
			for _, method := range grant.Methods {
				methods[method] = true
				// 外部接入以 access 身份转发白名单里的方法，编译路由时自动放行。
				c.acl[grant.Service][method] = insertSorted(c.acl[grant.Service][method], AccessCaller)
			}
			grants[grant.Service] = methods
		}
		c.principals[principal.ID] = grants
	}
	return nil
}

func (c *Catalog) buildConsoleRoutes() error {
	c.consoleRoutes = map[string]map[string]string{}
	for _, component := range c.Components {
		for _, service := range component.Services {
			if service.ConsoleName == "" {
				continue
			}
			routes := c.consoleRoutes[service.ConsoleName]
			if routes == nil {
				routes = map[string]string{}
				c.consoleRoutes[service.ConsoleName] = routes
			}
			allowed := 0
			for _, method := range service.RPCs {
				if !containsSorted(c.acl[service.Path][method], ConsoleCaller) {
					continue
				}
				if previous, exists := routes[method]; exists {
					return fmt.Errorf("console_name %s 的方法 %s 同时属于 %s 和 %s", service.ConsoleName, method, previous, service.Path)
				}
				routes[method] = service.Path
				allowed++
			}
			if allowed == 0 {
				return fmt.Errorf("服务 %s 声明了 console_name，但没有任何方法放行 %s", service.Path, ConsoleCaller)
			}
		}
	}
	return nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func containsSorted(values []string, value string) bool {
	index := sort.SearchStrings(values, value)
	return index < len(values) && values[index] == value
}

func insertSorted(values []string, value string) []string {
	index := sort.SearchStrings(values, value)
	if index < len(values) && values[index] == value {
		return values
	}
	values = append(values, "")
	copy(values[index+1:], values[index:])
	values[index] = value
	return values
}
