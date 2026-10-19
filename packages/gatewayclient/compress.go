package gatewayclient

import (
	"context"

	"github.com/mooyang-code/moox/packages/wirecompress"
)

type compressTypeKey struct{}

// WithCompressType 让转发原始字节的调用方（例如 Access）沿用入站帧的压缩类型：
// 调用方手里没有请求对象，无从得知空间，入站帧选的字典就是空间选的字典。
// 不是 wirecompress 注册过的类型（例如 0，未压缩）时忽略，按空间或默认字典处理。
func WithCompressType(ctx context.Context, compressType int) context.Context {
	if !wirecompress.Known(compressType) {
		return ctx
	}
	return context.WithValue(ctx, compressTypeKey{}, compressType)
}

// CompressTypeFromContext 返回 WithCompressType 沿用的压缩类型；没有沿用时 ok 为 false。
func CompressTypeFromContext(ctx context.Context) (compressType int, ok bool) {
	compressType, ok = ctx.Value(compressTypeKey{}).(int)
	return compressType, ok
}

// compressTypeFor 决定一次跨主机调用的压缩类型：显式沿用的入站类型优先，其次按空间选字典（context 里的
// wirecompress.WithSpace，由 Invoke 从请求顶层的 space_id 或调用点显式写入），都没有时用默认字典。
func compressTypeFor(ctx context.Context) int {
	if compressType, ok := CompressTypeFromContext(ctx); ok {
		return compressType
	}
	return wirecompress.CompressType(wirecompress.SpaceOf(ctx, nil))
}
