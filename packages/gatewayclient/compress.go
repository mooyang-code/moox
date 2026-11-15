package gatewayclient

import (
	"context"

	"github.com/mooyang-code/moox/packages/wirecompress"
)

type compressTypeKey struct{}

// WithCompressType 让转发原始字节的调用方（例如 Access）沿用入站帧的压缩类型。
// 不是 wirecompress 注册过的类型（例如 0，未压缩）时忽略，用当前字典版本。
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

// compressTypeFor 决定一次跨主机调用的压缩类型：显式沿用的入站类型优先，否则用当前字典版本。
func compressTypeFor(ctx context.Context) int {
	if compressType, ok := CompressTypeFromContext(ctx); ok {
		return compressType
	}
	return wirecompress.CompressType()
}
