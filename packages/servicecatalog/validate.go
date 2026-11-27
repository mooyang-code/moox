package servicecatalog

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
)

var (
	identifier  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	servicePath = regexp.MustCompile(`^trpc\.[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$`)
	methodName  = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	binaryName  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	placeholder = regexp.MustCompile(`\{[a-z]+\}`)
	hostLabel   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)
)

func (c Catalog) Validate() error {
	if c.Version != 2 || len(c.Components) == 0 || len(c.Components) > 64 {
		return fmt.Errorf("catalog requires version 2 and 1..64 components")
	}
	ids, binaries, paths := map[string]bool{}, map[string]bool{}, map[string]bool{}
	consoleRoutes := map[string]string{}
	components, external := map[string]bool{}, map[string]bool{}
	for _, component := range c.Components {
		components[component.ID], external[component.ID] = true, component.ExternalProgram
	}
	identities, err := c.validateIdentities(components)
	if err != nil {
		return err
	}
	for _, component := range c.Components {
		if !identifier.MatchString(component.ID) || component.Name == "" || component.Binary != "moox-"+component.ID {
			return fmt.Errorf("component %q requires a canonical ID, name and binary", component.ID)
		}
		if ids[component.ID] || binaries[component.Binary] {
			return fmt.Errorf("duplicate component %q", component.ID)
		}
		ids[component.ID], binaries[component.Binary] = true, true
		if !slices.Contains([]string{ScopeHost, ScopeControl, ScopeAny}, component.Scope) || component.Replicas != Single {
			return fmt.Errorf("component %q has invalid scope or replicas", component.ID)
		}
		// 外部程序可以声明服务（只监听本机，经所在主机的网关路由），但不能声明其他端口。
		if component.ExternalProgram && len(component.Ports) != 0 {
			return fmt.Errorf("external program %q cannot declare ports", component.ID)
		}
		ports := map[int]bool{}
		addPort := func(port int) error {
			if port < 1 || port > 65535 || ports[port] {
				return fmt.Errorf("component %q has invalid or duplicate port %d", component.ID, port)
			}
			ports[port] = true
			return nil
		}
		// 每个组件都有健康端口。
		if err := addPort(component.Health.Port); err != nil {
			return err
		}
		for _, port := range component.Ports {
			if err := addPort(port); err != nil {
				return err
			}
		}
		if component.RelayHTTP != nil {
			if !component.ExternalProgram {
				return fmt.Errorf("component %q declares relay_http but is not an external program", component.ID)
			}
			if component.RelayHTTP.Port == HostGatewayRemotePort {
				return fmt.Errorf("component %q relay_http cannot use the host gateway remote port", component.ID)
			}
			if err := addPort(component.RelayHTTP.Port); err != nil {
				return err
			}
		}
		for _, service := range component.Services {
			if !servicePath.MatchString(service.Path) || paths[service.Path] {
				return fmt.Errorf("invalid or duplicate service %q", service.Path)
			}
			paths[service.Path] = true
			if service.Port == 0 {
				if component.ID != adminComponent || service.ConsoleName == "" || !isConsoleOnlyService(service) {
					return fmt.Errorf("component %q service %q can use port 0 only for admin console-only services", component.ID, service.Path)
				}
			} else if err := addPort(service.Port); err != nil {
				return err
			}
			if service.TimeoutMS < 0 || service.TimeoutMS > 960000 || service.MaxBodyBytes < 0 || service.MaxBodyBytes > 64<<20 {
				return fmt.Errorf("service %q has invalid limits", service.Path)
			}
			if len(service.Methods) == 0 {
				return fmt.Errorf("service %q has no declared methods", service.Path)
			}
			methods := map[string]bool{}
			for _, method := range service.Methods {
				if !methodName.MatchString(method.Name) || methods[method.Name] {
					return fmt.Errorf("service %q has invalid or duplicate method %q", service.Path, method.Name)
				}
				methods[method.Name] = true
				if method.ReadOnly && method.Idempotent {
					return fmt.Errorf("service %q method %q is declared both read-only and idempotent", service.Path, method.Name)
				}
				if method.TimeoutMS < 0 || method.TimeoutMS > 960000 {
					return fmt.Errorf("service %q has invalid method timeout for %q", service.Path, method.Name)
				}
			}
			if service.ConsoleName != "" {
				if !identifier.MatchString(service.ConsoleName) {
					return fmt.Errorf("invalid console_name %q", service.ConsoleName)
				}
			}
		}
	}
	if err := c.validateDeployFields(ids); err != nil {
		return err
	}
	for _, component := range c.Components {
		if err := validateDoctor(component); err != nil {
			return err
		}
		if err := c.validateRelayHTTP(component, ids, external); err != nil {
			return err
		}
		for _, service := range component.Services {
			consoleAllowed := false
			for _, method := range service.Methods {
				if err := validateMethodCallers(service.Path, method, ids, identities); err != nil {
					return err
				}
				consoleAllowed = consoleAllowed || slices.Contains(method.OperatorList(), OperatorConsole)
			}
			if service.ConsoleName != "" && !consoleAllowed {
				return fmt.Errorf("console_name %q has no console method", service.ConsoleName)
			}
			if service.ConsoleName != "" {
				for _, method := range service.MethodNames() {
					if !c.Allowed(OperatorConsole, service.Path, method) {
						continue
					}
					key := service.ConsoleName + "/" + method
					if previous := consoleRoutes[key]; previous != "" {
						return fmt.Errorf("ambiguous console route %q for %s and %s", key, previous, service.Path)
					}
					consoleRoutes[key] = service.Path
				}
			}
		}
	}
	for _, id := range c.ExternalIdentities() {
		if len(c.Grants(id)) == 0 {
			return fmt.Errorf("external identity %q is not a caller of any method", id)
		}
	}
	return nil
}

// validateIdentities 校验 identities：ID 合法、不重复，不能是人操作身份、保留的调用方写法或组件 ID
// （组件用所在主机的密钥签名，同名的非主机身份会让持有身份私钥的人冒充组件）。目前只有外部身份。
func (c Catalog) validateIdentities(components map[string]bool) (map[string]bool, error) {
	identities := map[string]bool{}
	for _, identity := range c.Identities {
		reserved := slices.Contains([]string{OperatorConsole, OperatorMooxCLI, hostGatewayCaller}, identity.ID)
		if !identifier.MatchString(identity.ID) || identities[identity.ID] || reserved || components[identity.ID] {
			return nil, fmt.Errorf("invalid or duplicate identity %q", identity.ID)
		}
		if !identity.External {
			return nil, fmt.Errorf("identity %q must be external", identity.ID)
		}
		identities[identity.ID] = true
	}
	return identities, nil
}

// validateMethodCallers 校验方法的 callers 与 operators。callers 只能是组件或 identities 里的身份，不重复；
// 人操作的 console、moox-cli 只能写在 operators 里；host-gateway 只用于 GatewayControl。
func validateMethodCallers(servicePath string, method Method, ids, identities map[string]bool) error {
	seen := map[string]bool{}
	for _, caller := range method.Callers {
		if seen[caller] {
			return fmt.Errorf("service %q method %q has duplicate caller %q", servicePath, method.Name, caller)
		}
		seen[caller] = true
		switch {
		case caller == hostGatewayCaller:
			if servicePath != GatewayControlPath {
				return fmt.Errorf("host-gateway callers are restricted to GatewayControl")
			}
		case caller == OperatorConsole || caller == OperatorMooxCLI:
			return fmt.Errorf("service %q method %q lists %q in callers; use operators", servicePath, method.Name, caller)
		case !ids[caller] && !identities[caller]:
			return fmt.Errorf("service %q method %q has unknown caller %q", servicePath, method.Name, caller)
		}
	}
	if method.Operators != nil {
		seen := map[string]bool{}
		for _, operator := range *method.Operators {
			if seen[operator] || operator != OperatorConsole && operator != OperatorMooxCLI {
				return fmt.Errorf("service %q method %q has invalid operator %q", servicePath, method.Name, operator)
			}
			seen[operator] = true
		}
	}
	return nil
}

// validateRelayHTTP 校验外部程序的 relay_http：调用方只能是内置调用方（非外部组件、console、moox-cli），不重复；
// 端口不能与目录里任何其他组件的端口相同：中转许可只凭端口号认 relay_http 端口，撞上就有歧义。
func (c Catalog) validateRelayHTTP(component Component, ids, external map[string]bool) error {
	relay := component.RelayHTTP
	if relay == nil {
		return nil
	}
	if len(relay.Callers) == 0 || len(relay.Callers) > 16 {
		return fmt.Errorf("component %q relay_http requires 1..16 callers", component.ID)
	}
	seen := map[string]bool{}
	for _, caller := range relay.Callers {
		if seen[caller] {
			return fmt.Errorf("component %q relay_http has duplicate caller %q", component.ID, caller)
		}
		seen[caller] = true
		builtin := caller == "console" || caller == "moox-cli" || ids[caller] && !external[caller]
		if !builtin {
			return fmt.Errorf("component %q relay_http caller %q must be a built-in caller", component.ID, caller)
		}
	}
	for _, other := range c.Components {
		if other.ID != component.ID && slices.Contains(componentPorts(other), relay.Port) {
			return fmt.Errorf("component %q relay_http port %d is also used by %q", component.ID, relay.Port, other.ID)
		}
	}
	return nil
}

// isConsoleOnlyService 报告服务是否无机器调用方且全部方法的 operators 仅为 console。
// 只有这类纯控制台内部服务允许声明 port: 0（仅走 Admin 进程内 LocalDispatcher，不监听 TCP 端口）。
func isConsoleOnlyService(service Service) bool {
	if len(service.Methods) == 0 {
		return false
	}
	for _, method := range service.Methods {
		if len(method.Callers) > 0 || !slices.Equal(method.OperatorList(), []string{OperatorConsole}) {
			return false
		}
	}
	return true
}

func validateDoctor(component Component) error {
	d := component.Doctor
	if !slices.Contains([]string{"reporter", "host_snapshot", "delegated"}, d.Transport) || !slices.Contains([]string{"active", "deferred", "not_applicable"}, d.FunctionalObservability) {
		return fmt.Errorf("component %q has invalid doctor transport or observability", component.ID)
	}
	return nil
}

// ValidHostAddress accepts a bare IP or DNS host, without a scheme or port.
func ValidHostAddress(address string) bool {
	if address == "" || len(address) > 253 {
		return false
	}
	if net.ParseIP(address) != nil {
		return true
	}
	for _, label := range strings.Split(address, ".") {
		if len(label) > 63 || !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}
