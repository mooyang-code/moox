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
