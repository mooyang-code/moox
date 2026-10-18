package gatewayclient

import "context"

// CallMetadata carries trusted application context independently of the raw
// request body. Callers must authenticate the user and authorize the space
// before setting it. Gateway signing credentials cannot be overridden here.
type CallMetadata struct {
	SpaceID  string
	UserID   string
	UserRole string
	TraceID  string
}

type callMetadataKey struct{}

func WithCallMetadata(ctx context.Context, metadata CallMetadata) context.Context {
	return context.WithValue(ctx, callMetadataKey{}, metadata)
}

func CallMetadataFromContext(ctx context.Context) CallMetadata {
	metadata, _ := ctx.Value(callMetadataKey{}).(CallMetadata)
	return metadata
}
