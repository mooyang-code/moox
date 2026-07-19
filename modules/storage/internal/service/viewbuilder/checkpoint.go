package builder

import (
	"context"
	"errors"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// applyCheckpointOnly records a committed sequence even when a valid event
// has no rows for the selected projection (for example, a filtered batch).
// Without this checkpoint-only command, a no-op event permanently advances
// the in-memory lane while leaving the durable ViewIndex position behind.
func (s *Service) applyCheckpointOnly(ctx context.Context, progress applyProgress, engineName string) error {
	var views []*pb.View
	for pageNo := uint32(1); ; pageNo++ {
		items, page, err := s.metadata.ListViews(ctx, progress.spaceID, "", "active", &pb.Page{Page: pageNo, Size: 1000})
		if err != nil {
			return err
		}
		views = append(views, items...)
		if page == nil || !page.GetHasMore() || len(items) == 0 {
			break
		}
	}
	engine, err := s.engine(engineName)
	if err != nil {
		return err
	}
	for _, item := range views {
		if item == nil || !strings.EqualFold(item.GetEngine(), engineName) {
			continue
		}
		columns, _, err := s.metadata.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Size: 10000})
		if err != nil {
			return err
		}
		writable := writableIndexSet(item)
		activeID := item.GetActiveIndexId()
		if activeID != "" && writable[activeID] {
			activeColumns := item.GetActiveColumns()
			if len(activeColumns) == 0 {
				return errors.New("view " + item.GetViewId() + " has an active index without an active schema")
			}
			if err := applyViewIndexWithMode(ctx, engine, activeID, viewIndexBatch(item, activeColumns, nil, nil, false), nil, nil, progress, false); err != nil {
				return fmt.Errorf("checkpoint view %s active index: %w", item.GetViewId(), err)
			}
		}
		build := item.GetIndexBuild()
		buildID := build.GetIndexId()
		if buildID != "" && buildID != activeID && writable[buildID] {
			if err := applyViewIndexWithMode(ctx, engine, buildID, viewIndexBatch(item, columns, nil, nil, true), nil, nil, progress, false); err != nil {
				return fmt.Errorf("checkpoint view %s build index: %w", item.GetViewId(), err)
			}
		}
	}
	return nil
}
