// Package spacecontext 提供 space_id 在 HTTP/context 之间的注入与读取。
//
// 设计要点：
//   - space_id 是硬隔离维度，由 admin 网关 authorize filter 从登录态注入，
//     并经 forwardHTTP 透传 X-Space-Id 头到 trade 进程；trade 本进程的
//     spacectx server filter 再把该头写回 context，供业务层 FromContext 读取。
//   - 业务层不应接受 body 里的 space_id，防止越权。
package spacecontext

import (
	"context"
	"fmt"

	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/filter"
)

// SpaceIDHeader HTTP 头名，网关向 trade 透传 space_id 用。
const SpaceIDHeader = "X-Space-Id"

// SpaceFilterName 是 spacectx filter 在 trpc_go.yaml server.filter 中的注册名。
const SpaceFilterName = "spacectx"

type ctxKey struct{}

func init() {
	// 注册 server filter：从有协议 http 请求头 X-Space-Id 读取 space_id 注入 ctx。
	// 无 header / 非 http 请求时 noop。
	filter.Register(SpaceFilterName, spaceServerFilter, nil)
}

func spaceServerFilter(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
	if r := thttp.Request(ctx); r != nil {
		if sid := r.Header.Get(SpaceIDHeader); sid != "" {
			ctx = WithSpaceID(ctx, sid)
		}
	}
	return next(ctx, req)
}

// WithSpaceID 将 spaceID 写入 context。
func WithSpaceID(ctx context.Context, spaceID string) context.Context {
	if spaceID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, spaceID)
}

// FromContext 从 context 读取 spaceID，未设置返回 ("", false)。
func FromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	if !ok {
		return "", false
	}
	return v, v != ""
}

// MustFromContext 从 context 读取 spaceID，未设置返回错误。
func MustFromContext(ctx context.Context) (string, error) {
	spaceID, ok := FromContext(ctx)
	if !ok || spaceID == "" {
		return "", fmt.Errorf("space_id is required but not set in context")
	}
	return spaceID, nil
}
