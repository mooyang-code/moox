// Package spacecontext 取得 CloudNode 请求所属的 space：调用方（控制台、Collector）把它写在 tRPC 元数据
// x-space-id 中，进程内调用（测试、后台任务）可以直接放进 context。
package spacecontext

import (
	"context"
	"fmt"
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

// FromContext 读取 space_id：先看 context，再看 tRPC 元数据。
func FromContext(ctx context.Context) (string, bool) {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		spaceID := strings.TrimSpace(v)
		return spaceID, spaceID != ""
	}
	spaceID := strings.TrimSpace(string(trpc.GetMetaData(ctx, gatewayroute.MetadataSpaceID)))
	return spaceID, spaceID != ""
}

// MustFromContext 读取 space_id，缺失时返回错误。
func MustFromContext(ctx context.Context) (string, error) {
	spaceID, ok := FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("space_id is required but not set in context")
	}
	return spaceID, nil
}
