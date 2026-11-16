package servicecatalog

import "slices"

// hostGatewayCaller 是每台主机网关的调用方名字：网关用所在主机的密钥、以 host-gateway 的身份签名，签名主机另由签名头给出。
const hostGatewayCaller = "host-gateway"

// Allowed 报告调用方能否经主机网关的 11002、11003 调用这个方法，管理台与主机网关共用。
// 外部身份不走这两个端口，只能从外部入口（11004）进来，按 PrincipalAllowed 判断。
func (c Catalog) Allowed(caller, servicePath, method string) bool {
	m, ok := c.method(servicePath, method)
	if !ok {
		return false
	}
	switch {
	case caller == OperatorConsole || caller == OperatorMooxCLI:
		return slices.Contains(m.OperatorList(), caller)
	case c.ExternalIdentity(caller):
		return false
	}
	return slices.Contains(m.Callers, caller)
}

// PrincipalAllowed 报告外部身份能否调用这个方法：方法的 callers 里写了它。
func (c Catalog) PrincipalAllowed(principal, servicePath, method string) bool {
	m, ok := c.method(servicePath, method)
	return ok && c.ExternalIdentity(principal) && slices.Contains(m.Callers, principal)
}

func (c Catalog) method(servicePath, method string) (Method, bool) {
	service, ok := c.Service(servicePath)
	if !ok {
		return Method{}, false
	}
	return service.Method(method)
}

// ExternalIdentity 报告 id 是否是 identities 里的外部身份。
func (c Catalog) ExternalIdentity(id string) bool {
	return slices.ContainsFunc(c.Identities, func(identity Identity) bool { return identity.External && identity.ID == id })
}

// ExternalIdentities 按目录顺序返回全部外部身份。
func (c Catalog) ExternalIdentities() []string {
	ids := []string{}
	for _, identity := range c.Identities {
		if identity.External {
			ids = append(ids, identity.ID)
		}
	}
	return ids
}

// IdentityGrant 是一个身份在一个服务上能调的方法。
type IdentityGrant struct {
	Service string
	Methods []string
}

// Grants 按目录顺序返回身份能调的服务与方法（由各方法的 callers 推出）。
func (c Catalog) Grants(identity string) []IdentityGrant {
	grants := []IdentityGrant{}
	for _, component := range c.Components {
		for _, service := range component.Services {
			grant := IdentityGrant{Service: service.Path}
			for _, method := range service.Methods {
				if slices.Contains(method.Callers, identity) {
					grant.Methods = append(grant.Methods, method.Name)
				}
			}
			if len(grant.Methods) > 0 {
				grants = append(grants, grant)
			}
		}
	}
	return grants
}

func (c Catalog) ReadOnly(servicePath, method string) bool {
	m, ok := c.method(servicePath, method)
	return ok && m.ReadOnly
}

// Retryable 判断方法在请求可能已发出的网络失败后能否安全重发：只读方法，或声明为幂等的写方法。
func (c Catalog) Retryable(servicePath, method string) bool {
	m, ok := c.method(servicePath, method)
	return ok && (m.ReadOnly || m.Idempotent)
}
