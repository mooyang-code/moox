package gatewayclient

import "context"

// CallMetadata carries trusted application context independently of the raw
// request body. Callers must authenticate the user and authorize the space
// before setting it. Gateway signing credentials cannot be overridden here.
type CallMetadata struct {
	SpaceID string
	UserID  string
	TraceID string
	// HostID 指定把调用发到哪台主机；为空时按默认规则选择（本机优先，其次按主机 ID 排序）。
	// 只在隧道与直连模式下生效，不会作为请求头发送。
	HostID string
}

type callMetadataKey struct{}

func WithCallMetadata(ctx context.Context, metadata CallMetadata) context.Context {
	return context.WithValue(ctx, callMetadataKey{}, metadata)
}

func CallMetadataFromContext(ctx context.Context) CallMetadata {
	metadata, _ := ctx.Value(callMetadataKey{}).(CallMetadata)
	return metadata
}

type originCallerKey struct{}

// WithOriginCaller 只给入口主机的网关用：它验过外部身份的签名之后，以 host-gateway 的身份把请求转给别的主机，
// 并用 X-Moox-Origin-Caller 头告诉目标网关原来是哪个外部身份。目标网关对这个头另有三条检查，别的调用方带上它只会被拒。
func WithOriginCaller(ctx context.Context, identity string) context.Context {
	return context.WithValue(ctx, originCallerKey{}, identity)
}

func originCallerFrom(ctx context.Context) string {
	identity, _ := ctx.Value(originCallerKey{}).(string)
	return identity
}
