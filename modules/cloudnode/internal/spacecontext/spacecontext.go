// Package spacecontext provides space_id injection from gateway headers.
package spacecontext

import (
	"context"
	"fmt"

	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/filter"
)

const (
	// SpaceIDHeader is the HTTP header used by admin gateway forwarding.
	SpaceIDHeader = "X-Space-Id"
	// SpaceFilterName is the trpc server filter registration name.
	SpaceFilterName = "spacectx"
)

type ctxKey struct{}

func init() {
	filter.Register(SpaceFilterName, spaceServerFilter, nil)
}

func spaceServerFilter(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
	if r := thttp.Request(ctx); r != nil {
		if sid := r.Header.Get(SpaceIDHeader); sid != "" {
			ctx = WithSpaceID(ctx, sid)
		}
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
	if !ok {
		return "", false
	}
	return v, v != ""
}

// MustFromContext reads space_id or returns an error.
func MustFromContext(ctx context.Context) (string, error) {
	spaceID, ok := FromContext(ctx)
	if !ok || spaceID == "" {
		return "", fmt.Errorf("space_id is required but not set in context")
	}
	return spaceID, nil
}
