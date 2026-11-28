// Package spacecontext 读取由管理台或主机网关经 tRPC 元数据转发的空间标识（X-Space-Id），
// 并注册服务端过滤器 spacectx 将其写入 context。
//
// 各业务模块读取空间的分界如下：
//   - Strategy、Trade：只读请求头（X-Space-Id），业务方法必须携带空间，由本包统一提取。
//   - Collector、Storage、Archive：读请求体（space_id），那里的空间同时是数据标识或过滤条件
//     （Archive 额外读请求头做范围校验）。
//   - Factor：请求头为可选的范围限定，有头按头限定、无头不限（内部服务、因子引擎与 setup verify
//     跨空间列任务走无头）；任务对象中的 space_id 是对象属性，有头时与头比对。
package spacecontext

import (
	"context"
	"strings"

	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
)

// SpaceIDHeader 是网关与管理台转发空间标识使用的元数据键名。
const SpaceIDHeader = "X-Space-Id"

// SpaceFilterName 是注册到 tRPC 服务端的过滤器名称。
const SpaceFilterName = "spacectx"

type ctxKey struct{}

func init() { filter.Register(SpaceFilterName, spaceServerFilter, nil) }

func spaceServerFilter(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
	if spaceID, ok := FromContext(ctx); ok {
		ctx = WithSpaceID(ctx, spaceID)
	}
	return next(ctx, req)
}

// WithSpaceID 将 space_id 写入 context；空串原样返回。
func WithSpaceID(ctx context.Context, spaceID string) context.Context {
	if spaceID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, spaceID)
}

// FromContext 从 context 读取 space_id：优先取 WithSpaceID 写入的值，否则读取 tRPC ServerMetaData 中的 X-Space-Id；
// 大小写别名同时出现且值冲突时返回空与 false。
func FromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	if ok {
		spaceID := strings.TrimSpace(v)
		return spaceID, spaceID != ""
	}
	var spaceID string
	var found bool
	for key, value := range codec.Message(ctx).ServerMetaData() {
		if !strings.EqualFold(key, SpaceIDHeader) {
			continue
		}
		candidate := strings.TrimSpace(string(value))
		if found && candidate != spaceID {
			return "", false
		}
		spaceID, found = candidate, true
	}
	return spaceID, spaceID != ""
}
