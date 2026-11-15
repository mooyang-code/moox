package servicecatalog

import (
	"fmt"
	"net"
	"path"
	"regexp"
	"slices"
	"strings"
)

var (
	identifier  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	servicePath = regexp.MustCompile(`^trpc\.[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+$`)
	methodName  = regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	unitName    = regexp.MustCompile(`^moox-[a-z0-9]+(?:-[a-z0-9]+)*$`)
	hostLabel   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)
)

func (c Catalog) Validate() error {
	if c.Version != 2 || len(c.Components) == 0 || len(c.Components) > 64 {
		return fmt.Errorf("catalog requires version 2 and 1..64 components")
	}
	ids, binaries, paths := map[string]bool{}, map[string]bool{}, map[string]bool{}
	consoleRoutes := map[string]string{}
	external := map[string]bool{}
	for _, component := range c.Components {
		external[component.ID] = component.ExternalProgram
	}
	identities, err := c.validateIdentities(external)
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
		if !slices.Contains([]string{ScopeHost, ScopeControl, ScopeAny}, component.Scope) || !slices.Contains([]string{Single, Multi}, component.Replicas) {
			return fmt.Errorf("component %q has invalid scope or replicas", component.ID)
		}
		// Host components have one instance on each host. The single limit is
		// per host for this scope, rather than one instance for the whole fleet.
		if component.Scope == ScopeHost && component.Replicas != Single {
			return fmt.Errorf("host component %q must be single per host", component.ID)
		}
		if !slices.Contains([]string{"readyz", "https", "none"}, component.Health.Kind) {
			return fmt.Errorf("component %q has invalid health.kind", component.ID)
		}
		// 外部程序可以声明服务（只监听本机，经所在主机的网关路由），但不能声明其他端口。
		if component.ExternalProgram {
			if len(component.Ports) != 0 || component.Health.Kind != "readyz" || component.Health.Loopback {
				return fmt.Errorf("external program %q cannot declare ports and must use a readyz health port", component.ID)
			}
		}
		ports := map[int]bool{}
		addPort := func(port int) error {
			if port < 1 || port > 65535 || ports[port] {
				return fmt.Errorf("component %q has invalid or duplicate port %d", component.ID, port)
			}
			ports[port] = true
			return nil
		}
		if component.Health.Kind == "none" {
			if component.Health.Port != 0 {
				return fmt.Errorf("health none cannot have a port")
			}
		} else if err := addPort(component.Health.Port); err != nil {
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
			if err := addPort(service.Port); err != nil {
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
	for _, component := range c.Components {
		if err := validateDoctor(component, ids); err != nil {
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
	for _, component := range c.Components {
		if component.ExternalProgram && !c.ExternalIdentity(component.ID) {
			return fmt.Errorf("external program %q requires an external identity with the same ID", component.ID)
		}
	}
	if len(c.ExternalIdentities()) != 0 && !ids["access"] {
		return fmt.Errorf("external identities require the access component")
	}
	return nil
}

// validateIdentities 校验 identities：ID 合法、不重复，不能是人操作身份或保留的调用方写法；
// 与组件同名时那个组件必须是外部程序（它以同名外部身份访问系统）。目前只有外部身份。
func (c Catalog) validateIdentities(external map[string]bool) (map[string]bool, error) {
	identities := map[string]bool{}
	for _, identity := range c.Identities {
		reserved := slices.Contains([]string{OperatorConsole, OperatorMooxCLI, "access", hostGatewayCaller}, identity.ID)
		program, component := external[identity.ID]
		if !identifier.MatchString(identity.ID) || identities[identity.ID] || reserved || component && !program {
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
// 人操作的 console、moox-cli 只能写在 operators 里；access 由外部身份推出，不能直接写；host-gateway 只用于 GatewayControl。
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
		case caller == "access":
			return fmt.Errorf("service %q method %q lists access; it follows from external identities", servicePath, method.Name)
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
// 端口不能与目录里任何其他组件的端口相同，ValidRelay 只凭端口号区分健康端口与 relay_http 端口，撞上就有歧义。
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

func validateDoctor(component Component, ids map[string]bool) error {
	d := component.Doctor
	if d.Role == "" || d.Description == "" || len(d.Duties) == 0 || len(d.Inputs) == 0 || len(d.Outputs) == 0 {
		return fmt.Errorf("component %q has incomplete doctor metadata", component.ID)
	}
	if !slices.Contains([]string{"reporter", "host_snapshot", "health_only"}, d.Transport) || !slices.Contains([]string{"active", "deferred", "not_applicable"}, d.FunctionalObservability) {
		return fmt.Errorf("component %q has invalid doctor transport or observability", component.ID)
	}
	if err := ValidateSupervisor(d.Supervisor, d.Unit); err != nil {
		return fmt.Errorf("component %q: %w", component.ID, err)
	}
	if component.ExternalProgram && d.Supervisor != SupervisorSystemdUser {
		return fmt.Errorf("external program %q must be supervised by systemd_user", component.ID)
	}
	seen := map[string]bool{}
	for _, dep := range d.Dependencies {
		if !ids[dep] || dep == component.ID || seen[dep] {
			return fmt.Errorf("component %q has unknown, duplicate or self dependency %q", component.ID, dep)
		}
		seen[dep] = true
	}
	for _, p := range append(slices.Clone(d.ConfigPaths), d.WritablePaths...) {
		if p == "" || path.IsAbs(p) || path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\\x00\r\n") {
			return fmt.Errorf("component %q has unsafe doctor path", component.ID)
		}
	}
	allowed := []string{"verify_service_identity", "repair_path_permissions", "verify_eventbus_credentials", "restart_service_manually", "inspect_health_check_input", "replay_factor_window_manually", "free_disk_space", "run_bootstrap"}
	for _, action := range d.RecoveryActionIDs {
		if !slices.Contains(allowed, action) {
			return fmt.Errorf("component %q has unknown recovery action %q", component.ID, action)
		}
	}
	return nil
}

// ValidateSupervisor 校验进程托管方式与单元名的组合：systemd_user 必须带合法的单元名，其余托管方式不能带单元名。
func ValidateSupervisor(supervisor, unit string) error {
	switch supervisor {
	case "", SupervisorMooxRuntime:
		if unit != "" {
			return fmt.Errorf("unit is only valid for supervisor %s", SupervisorSystemdUser)
		}
	case SupervisorSystemdUser:
		if !unitName.MatchString(unit) {
			return fmt.Errorf("supervisor %s requires a valid unit name", SupervisorSystemdUser)
		}
	default:
		return fmt.Errorf("unknown supervisor %q", supervisor)
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
