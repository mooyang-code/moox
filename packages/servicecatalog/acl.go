package servicecatalog

import "slices"

// hostGatewayCaller 是每台主机网关的调用方名字：网关用所在主机的密钥、以 host-gateway 的身份签名，签名主机另由签名头给出。
const hostGatewayCaller = "host-gateway"

// Allowed 报告调用方能否经主机网关调用这个方法，管理台与主机网关共用。外部身份不能直接到主机网关，只能由 access 代为转发。
func (c Catalog) Allowed(caller, servicePath, method string) bool {
	m, ok := c.method(servicePath, method)
	if !ok {
		return false
	}
	switch {
	case caller == OperatorConsole || caller == OperatorMooxCLI:
		return slices.Contains(m.OperatorList(), caller)
	case caller == "access":
		return c.externallyCallable(m)
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

func (c Catalog) externallyCallable(m Method) bool {
	return slices.ContainsFunc(m.Callers, c.ExternalIdentity)
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

// MissingGrant 是一项授权服务没有部署：外部身份能调它，但提供它的组件不在部署里。
type MissingGrant struct {
	Principal string `json:"principal"`
	Service   string `json:"service"`
	Component string `json:"component"`
}

// MissingGrants 列出外部身份能调、但没有部署的服务。Access 要等全部授权服务都进入服务目录才就绪，
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
	for _, principal := range c.ExternalIdentities() {
		for _, grant := range c.Grants(principal) {
			provider := providers[grant.Service]
			if seen[grant.Service] || slices.Contains(deployed, provider) {
				continue
			}
			seen[grant.Service] = true
			missing = append(missing, MissingGrant{Principal: principal, Service: grant.Service, Component: provider})
		}
	}
	return missing
}
