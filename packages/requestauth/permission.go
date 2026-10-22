package requestauth

import "context"

// Permission 是请求经鉴权后带上的权限。权限由**服务端**的密钥映射决定（不接受客户端自称）：
// 同一个健康端口可以配置多对密钥，每对密钥对应一组权限。
type Permission string

// PermissionReset 允许触发破坏性的重置（删除业务数据与配置）。只映射给运维用的专用密钥，
// 模块之间互相调用用的运行时密钥没有这个权限。
const PermissionReset Permission = "reset"

type permissionsKey struct{}

// WithPermissions 返回带上权限的上下文，由鉴权中间件在验签通过后设置。
func WithPermissions(ctx context.Context, permissions ...Permission) context.Context {
	if len(permissions) == 0 {
		return ctx
	}
	return context.WithValue(ctx, permissionsKey{}, append([]Permission(nil), permissions...))
}

// HasPermission 报告上下文是否带有某个权限。
func HasPermission(ctx context.Context, want Permission) bool {
	granted, _ := ctx.Value(permissionsKey{}).([]Permission)
	for _, p := range granted {
		if p == want {
			return true
		}
	}
	return false
}
