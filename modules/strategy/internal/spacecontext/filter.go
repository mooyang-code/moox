package spacecontext

import (
	"context"

	"trpc.group/trpc-go/trpc-go/filter"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go"
)

const SpaceFilterName = "spacectx"

func init() {
	filter.Register(SpaceFilterName, spaceServerFilter, nil)
}

func spaceServerFilter(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
	if request := thttp.Request(ctx); request != nil {
		if spaceID := request.Header.Get(SpaceIDHeader); spaceID != "" {
			ctx = WithSpaceID(ctx, spaceID)
			trpc.SetMetaData(ctx, SpaceIDHeader, []byte(spaceID))
			trpc.SetMetaData(ctx, "space_id", []byte(spaceID))
		}
	}
	return next(ctx, req)
}
