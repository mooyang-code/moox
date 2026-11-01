package servicecatalog

import (
	"slices"
	"strings"
)

// Allowed is shared by the console and host gateways. External principal IDs
// never authenticate to a host gateway: only access may forward their requests.
func (c Catalog) Allowed(caller, servicePath, method string) bool {
	if caller == "host-gateway@*" {
		return false
	}
	service, ok := c.Service(servicePath)
	if !ok || !slices.Contains(service.Methods, method) {
		return false
	}
	if caller == "access" && c.externallyAllowed(servicePath, method) {
		return true
	}
	for _, grant := range service.ACL {
		if !slices.Contains(grant.Methods, method) {
			continue
		}
		for _, allowed := range grant.Callers {
			if caller == allowed {
				return true
			}
			if allowed == "host-gateway@*" && strings.HasPrefix(caller, "host-gateway@") && identifier.MatchString(strings.TrimPrefix(caller, "host-gateway@")) {
				return true
			}
		}
	}
	return false
}

func (c Catalog) PrincipalAllowed(principal, servicePath, method string) bool {
	for _, p := range c.Principals {
		if p.ID != principal {
			continue
		}
		for _, permission := range p.Allow {
			if permission.Service == servicePath && slices.Contains(permission.Methods, method) {
				return true
			}
		}
	}
	return false
}

func (c Catalog) externallyAllowed(servicePath, method string) bool {
	for _, p := range c.Principals {
		if c.PrincipalAllowed(p.ID, servicePath, method) {
			return true
		}
	}
	return false
}

func (c Catalog) ReadOnly(servicePath, method string) bool {
	service, ok := c.Service(servicePath)
	return ok && slices.Contains(service.ReadOnlyMethods, method)
}

// Retryable 判断方法在请求可能已发出的网络失败后能否安全重发：只读方法，或声明为幂等的写方法。
func (c Catalog) Retryable(servicePath, method string) bool {
	service, ok := c.Service(servicePath)
	return ok && (slices.Contains(service.ReadOnlyMethods, method) || slices.Contains(service.IdempotentMethods, method))
}

// MissingGrant 是一项授权服务没有部署：调用方的授权指向了它，但提供它的组件不在部署里。
type MissingGrant struct {
	Principal string `json:"principal"`
	Service   string `json:"service"`
	Component string `json:"component"`
}

// MissingGrants 列出授权服务里没有部署的项。Access 要等全部授权服务都进入服务目录才就绪，
// 所以这里缺一项，部署时就会卡在 Access 未就绪。
func (c Catalog) MissingGrants(deployed []string) []MissingGrant {
	providers := map[string]string{}
	for _, component := range c.Components {
		for _, service := range component.Services {
			providers[service.Path] = component.ID
		}
	}
	missing := []MissingGrant{}
	seen := map[string]bool{}
	for _, principal := range c.Principals {
		for _, grant := range principal.Allow {
			provider := providers[grant.Service]
			if seen[grant.Service] || slices.Contains(deployed, provider) {
				continue
			}
			seen[grant.Service] = true
			missing = append(missing, MissingGrant{Principal: principal.ID, Service: grant.Service, Component: provider})
		}
	}
	return missing
}
