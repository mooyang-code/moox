package servicecatalog

// Allowed 判断调用方能否调用某个服务的方法。主机网关（经快照中编译好的路由）和控制台（进程内调用时）
// 共用这一份判定，两边结果一致。host-gateway@<主机> 匹配 ACL 中的 host-gateway。
func (c *Catalog) Allowed(caller, servicePath, method string) bool {
	callers, ok := c.acl[servicePath][method]
	if !ok {
		return false
	}
	key := caller
	if _, isGateway := HostOfGatewayIdentity(caller); isGateway {
		key = HostGatewayCaller
	} else if caller == HostGatewayCaller {
		// 裸前缀不是合法身份，必须带上主机 ID。
		return false
	}
	return containsSorted(callers, key)
}

// KnownCaller 判断一个签名身份是否为目录中定义的内部调用方：组件 ID、callers 中的身份，
// 或 host-gateway@<主机>。外部调用方只在外部接入上校验，不是内部调用方。
func (c *Catalog) KnownCaller(caller string) bool {
	if _, isGateway := HostOfGatewayIdentity(caller); isGateway {
		return true
	}
	if caller == HostGatewayCaller {
		return false
	}
	if _, ok := c.components[caller]; ok {
		return true
	}
	for _, item := range c.Callers {
		if item.ID == caller {
			return true
		}
	}
	return false
}
