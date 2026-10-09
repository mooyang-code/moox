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
	hostLabel   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)
)

func (c Catalog) Validate() error {
	if c.Version != 1 || len(c.Components) == 0 || len(c.Components) > 64 {
		return fmt.Errorf("catalog requires version 1 and 1..64 components")
	}
	ids, binaries, paths := map[string]bool{}, map[string]bool{}, map[string]bool{}
	consoleRoutes := map[string]string{}
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
				if !methodName.MatchString(method) || methods[method] {
					return fmt.Errorf("service %q has invalid or duplicate method %q", service.Path, method)
				}
				methods[method] = true
			}
			if err := declaredMethods(service.ReadOnlyMethods, methods); err != nil {
				return fmt.Errorf("service %q read-only methods: %w", service.Path, err)
			}
			for _, grant := range service.ACL {
				if len(grant.Methods) == 0 || len(grant.Callers) == 0 {
					return fmt.Errorf("service %q has empty ACL grant", service.Path)
				}
				if err := declaredMethods(grant.Methods, methods); err != nil {
					return fmt.Errorf("service %q ACL: %w", service.Path, err)
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
		for _, service := range component.Services {
			consoleAllowed := false
			for _, grant := range service.ACL {
				seen := map[string]bool{}
				for _, caller := range grant.Callers {
					if seen[caller] {
						return fmt.Errorf("duplicate ACL caller %q", caller)
					}
					seen[caller] = true
					if caller == "host-gateway@*" {
						if service.Path != GatewayControlPath {
							return fmt.Errorf("host-gateway instance ACL is restricted to GatewayControl")
						}
					} else if !ids[caller] && caller != "console" && caller != "moox-cli" {
						return fmt.Errorf("service %q has unknown caller %q", service.Path, caller)
					}
					if caller == "console" {
						consoleAllowed = true
					}
				}
			}
			if service.ConsoleName != "" && !consoleAllowed {
				return fmt.Errorf("console_name %q has no console method", service.ConsoleName)
			}
			if service.ConsoleName != "" {
				for _, method := range service.Methods {
					if !c.Allowed("console", service.Path, method) {
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
	principals := map[string]bool{}
	for _, principal := range c.Principals {
		if !slices.Contains([]string{"scf-collector", "factor-engine", "moox-skill"}, principal.ID) || principals[principal.ID] || ids[principal.ID] || len(principal.Allow) == 0 {
			return fmt.Errorf("invalid or duplicate external principal %q", principal.ID)
		}
		principals[principal.ID] = true
		seen := map[string]bool{}
		for _, permission := range principal.Allow {
			service, ok := c.Service(permission.Service)
			if !ok || seen[permission.Service] || len(permission.Methods) == 0 {
				return fmt.Errorf("principal %q references unknown or duplicate service %q", principal.ID, permission.Service)
			}
			seen[permission.Service] = true
			declared := map[string]bool{}
			for _, method := range service.Methods {
				declared[method] = true
			}
			if err := declaredMethods(permission.Methods, declared); err != nil {
				return fmt.Errorf("principal %q: %w", principal.ID, err)
			}
		}
	}
	if len(c.Principals) != 0 && !ids["access"] {
		return fmt.Errorf("external principals require the access component")
	}
	return nil
}

func declaredMethods(methods []string, declared map[string]bool) error {
	seen := map[string]bool{}
	for _, method := range methods {
		if !declared[method] || seen[method] {
			return fmt.Errorf("undeclared or duplicate method %q", method)
		}
		seen[method] = true
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

func validAddress(address string) bool {
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
