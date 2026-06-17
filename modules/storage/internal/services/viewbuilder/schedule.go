package viewbuilder

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

var defaultBuilder struct {
	sync.RWMutex
	value *Builder
}

func SetDefaultBuilder(builder *Builder) {
	defaultBuilder.Lock()
	defer defaultBuilder.Unlock()
	defaultBuilder.value = builder
}

func HandleSchedule(ctx context.Context, params string) error {
	builder := currentDefaultBuilder()
	if builder == nil {
		log.WarnContext(ctx, "[ViewBuilder] default builder is not initialized, skip schedule")
		return nil
	}
	spaceID := scheduleSpaceID(params)
	var (
		built []*pb.View
		err   error
	)
	if spaceID != "" {
		built, err = builder.RebuildPendingViews(ctx, spaceID)
	} else {
		built, err = builder.RebuildPendingViewsInAllSpaces(ctx)
	}
	if err != nil {
		log.ErrorContextf(ctx, "[ViewBuilder] schedule failed: %v", err)
		return err
	}
	log.InfoContextf(ctx, "[ViewBuilder] schedule rebuilt %d view(s)", len(built))
	return nil
}

func currentDefaultBuilder() *Builder {
	defaultBuilder.RLock()
	defer defaultBuilder.RUnlock()
	return defaultBuilder.value
}

func (b *Builder) RebuildPendingViewsInAllSpaces(ctx context.Context) ([]*pb.View, error) {
	if b == nil || b.metadata == nil {
		return nil, errMetadataRequired()
	}
	const pageSize = uint32(1000)
	var built []*pb.View
	for pageNo := uint32(1); ; pageNo++ {
		spaces, page, err := b.metadata.ListSpaces(ctx, "", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		for _, space := range spaces {
			items, err := b.RebuildPendingViews(ctx, space.GetSpaceId())
			if err != nil {
				return nil, err
			}
			built = append(built, items...)
		}
		if page == nil || !page.GetHasMore() {
			return built, nil
		}
	}
}

func scheduleSpaceID(params string) string {
	params = strings.TrimSpace(params)
	if params == "" {
		return ""
	}
	values, err := url.ParseQuery(strings.TrimPrefix(params, "?"))
	if err == nil {
		if spaceID := strings.TrimSpace(values.Get("space_id")); spaceID != "" {
			return spaceID
		}
	}
	if !strings.Contains(params, "=") {
		return params
	}
	return ""
}

func errMetadataRequired() error {
	return errors.New("metadata is required")
}
