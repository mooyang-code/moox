// Package spacecontext reads the space identity forwarded by the native gateway.
package spacecontext

import (
	"context"
	"fmt"
	"strings"

	"trpc.group/trpc-go/trpc-go/codec"
)

// SpaceIDHeader is the metadata key used by gateway forwarding.
const SpaceIDHeader = "X-Space-Id"

type ctxKey struct{}

// WithSpaceID stores space_id in context.
func WithSpaceID(ctx context.Context, spaceID string) context.Context {
	if spaceID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, spaceID)
}

// FromContext reads space_id from context.
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

// MustFromContext reads space_id or returns an error.
func MustFromContext(ctx context.Context) (string, error) {
	spaceID, ok := FromContext(ctx)
	if !ok || spaceID == "" {
		return "", fmt.Errorf("space_id is required but not set in context")
	}
	return spaceID, nil
}
