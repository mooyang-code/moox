// Package spacecontext 提供 space_id 在 HTTP/context 之间的注入与读取。
// 设计要点：
//   - space_id 是硬隔离维度，必须由网关/中间件从登录态或请求头注入，
//     业务层不应接受 body 里的 space_id，防止越权。
//   - HTTP 层：SpaceIDHeader = "X-Space-Id"。
//   - 跨层传递：通过 context.Context 携带，避免在每个方法签名里冗余传参。
package spacecontext

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
)

// SpaceIDHeader HTTP 头名，用于在网关层注入当前空间
const SpaceIDHeader = "X-Space-Id"

// ctxKey 私有 key 类型，避免与其他包冲突
type ctxKey struct{}

// WithSpaceID 将 spaceID 写入 context
func WithSpaceID(ctx context.Context, spaceID string) context.Context {
	if spaceID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, spaceID)
}

// FromContext 从 context 读取 spaceID，未设置返回 ("", false)
func FromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	if !ok {
		return "", false
	}
	return v, v != ""
}

// MustFromContext 从 context 读取 spaceID，未设置返回错误
// 用于 service 层强制要求 space_id 的场景
func MustFromContext(ctx context.Context) (string, error) {
	spaceID, ok := FromContext(ctx)
	if !ok || spaceID == "" {
		return "", fmt.Errorf("space_id is required but not set in context")
	}
	return spaceID, nil
}

// FromGin 从 gin.Context 读取 space_id，优先从 header 取，回退到 query
// 用于 handler 层注入前提取
func FromGin(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if v := c.GetHeader(SpaceIDHeader); v != "" {
		return v
	}
	if v := c.Query("space_id"); v != "" {
		return v
	}
	return ""
}

// InjectToGinContext 从 gin.Context 提取 space_id 并写入 request context
// 返回带有 space_id 的 context，供 service 层使用
func InjectToGinContext(c *gin.Context) context.Context {
	if c == nil {
		return context.Background()
	}
	return WithSpaceID(c.Request.Context(), FromGin(c))
}
