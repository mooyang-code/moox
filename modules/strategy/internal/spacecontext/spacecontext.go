package spacecontext

import "context"

const SpaceIDHeader = "X-Space-Id"

type spaceIDKey struct{}

func WithSpaceID(ctx context.Context, spaceID string) context.Context {
	return context.WithValue(ctx, spaceIDKey{}, spaceID)
}

func FromContext(ctx context.Context) string {
	if value, _ := ctx.Value(spaceIDKey{}).(string); value != "" {
		return value
	}
	return ""
}
