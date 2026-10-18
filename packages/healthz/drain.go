package healthz

import (
	"context"
	"sync/atomic"

	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
)

// DrainGate 在停止排空期间拒绝新的 RPC。tRPC 的 MaxCloseWaitTime 只会等待已进入的请求，
// 不会阻止同一连接或新连接上的后续请求，排空窗口内仍可能接纳新的写操作。
type DrainGate struct{ draining atomic.Bool }

// Begin 开始排空：此后到达的请求在进入业务处理前被拒绝，已接纳的请求继续执行。
func (g *DrainGate) Begin() { g.draining.Store(true) }

func (g *DrainGate) Draining() bool { return g.draining.Load() }

// Filter 返回服务端过滤器。拒绝使用“服务不存在”类错误码，调用方据此刷新目录并把只读请求改投其他主机；
// 请求尚未执行，写请求不会被误判为已提交。
func (g *DrainGate) Filter() filter.ServerFilter {
	return func(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
		if g.draining.Load() {
			return nil, errs.NewFrameError(errs.RetServerNoService, "service is draining for shutdown")
		}
		return next(ctx, req)
	}
}
