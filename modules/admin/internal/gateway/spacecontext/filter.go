package spacecontext

import (
	"context"

	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/filter"
)

// SpaceFilterName 是 space_id 注入 filter 在 trpc_go.yaml server.filter 中的注册名。
const SpaceFilterName = "spacectx"

func init() {
	// 注册 server filter：从有协议 http 请求头 X-Space-Id 读取 space_id 注入 ctx，
	// 供本进程业务服务通过 FromContext 读取。无 header / 非 http 请求时 noop。
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
