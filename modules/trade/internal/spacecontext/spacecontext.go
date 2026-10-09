// Package spacecontext 取得 Trade 请求所属的 space：调用方（控制台、Strategy）把它写在 tRPC 元数据
// x-space-id 中，主机网关原样透传；进程内调用（测试、后台任务）可以直接放进 context。
// 业务层不接受请求体里的 space_id，防止越权。
package spacecontext

import (
	"context"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayroute"
	trpc "trpc.group/trpc-go/trpc-go"
)

type ctxKey struct{}

// WithSpaceID 把 space_id 放进 context，优先于 tRPC 元数据。
func WithSpaceID(ctx context.Context, spaceID string) context.Context {
	if spaceID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, spaceID)
}

// FromContext 读取 space_id：先看 context，再看 tRPC 元数据；未设置时返回 ("", false)。
func FromContext(ctx context.Context) (string, bool) {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		spaceID := strings.TrimSpace(v)
		return spaceID, spaceID != ""
	}
	spaceID := strings.TrimSpace(string(trpc.GetMetaData(ctx, gatewayroute.MetadataSpaceID)))
	return spaceID, spaceID != ""
}
