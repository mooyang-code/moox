package gatewayroute

// 主机网关返回给调用方的业务错误码。gatewayclient 据此决定是否刷新服务目录、是否重试。
const (
	// RetUnauthenticated 表示签名校验失败、密钥未知或请求被重放。
	RetUnauthenticated = 4401
	// RetForbidden 表示调用方不在路由的 ACL 中。
	RetForbidden = 4403
	// RetServiceNotHere 表示服务不在本机，或它在本机的部署已停用；调用方应立即刷新服务目录。
	RetServiceNotHere = 4404
	// RetBodyTooLarge 表示请求或响应超过路由的包体上限。
	RetBodyTooLarge = 4413
	// RetHostDisabled 表示本机已停用。
	RetHostDisabled = 4503
)
