// Package spacecontext reads the space identity forwarded by the native gateway.
package spacecontext

import (
	"context"
	"strings"

	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
)

// SpaceIDHeader is the metadata key used by gateway forwarding.
const SpaceIDHeader = "X-Space-Id"

const SpaceFilterName = "spacectx"

type ctxKey struct{}

func init() { filter.Register(SpaceFilterName, spaceServerFilter, nil) }

func spaceServerFilter(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
	if spaceID, ok := FromContext(ctx); ok {
		ctx = WithSpaceID(ctx, spaceID)
	}
	return next(ctx, req)
}

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
