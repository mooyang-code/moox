package view

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"

	"trpc.group/trpc-go/trpc-go/log"
)

// extendViewSchemasRound 给数据集新加了列的 View 原地加列，由每分钟的定时任务触发。新列默认为空，不回填。
func (s *Service) extendViewSchemasRound(ctx context.Context) {
	metadata := s.metadataClientSnapshot()
	if metadata == nil {
		return
	}
	auth := s.internalAuth()
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadata.ListViews(ctx, &pb.ListViewsReq{AuthInfo: auth, Status: "active", Page: &pb.Page{Page: pageNo, Size: 100}})
		if err == nil {
			err = requireSuccess(rsp.GetRetInfo())
		}
		if err != nil {
			log.Errorf("storage view 检查加列时读取 View 失败：%v", err)
			return
		}
		for _, view := range rsp.GetViews() {
			if ctx.Err() != nil {
				return
			}
			if view == nil {
				continue
			}
			revision, err := s.extendViewSchema(ctx, view)
			if err != nil {
				log.Errorf("storage view 加列失败 space=%s view=%s：%v", view.GetSpaceId(), view.GetViewId(), err)
			} else if revision > 0 {
				log.Infof("storage view 加列完成 space=%s view=%s 修订号=%d", view.GetSpaceId(), view.GetViewId(), revision)
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			return
		}
	}
}

// extendViewSchema 把 View 声明里还没建进活动索引的列原地加进去，返回加列后的修订号；没有要加的列时返回 0。
// 当前契约取内存里活动索引的契约（与索引文件一致），加列和新契约在索引的一次提交里落盘。
func (s *Service) extendViewSchema(ctx context.Context, view *pb.View) (uint64, error) {
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	indexID := viewindex.ViewIndexID(view.GetSpaceId(), view.GetViewId())
	desired := view.GetColumns()
	s.mu.RLock()
	runtime := s.views[viewKey]
	current := s.schemas[indexID]
	s.mu.RUnlock()
	// 先按快照判断有没有要加的列：每分钟都要检查所有 View，绝大多数没有新列，不用取锁。
	if runtime == nil {
		return 0, nil
	}
	if _, ok := viewSchemaExtensionTarget(current, desired); !ok {
		return 0, nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.active != indexID {
		return 0, nil
	}
	release, err := s.indexWriteGate(indexID).lock(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	// 定时任务和建索引都在这两把锁下改契约：持锁后重取一次，用过期的快照算目标会把刚提交的修订号再加一遍列。
	s.mu.RLock()
	current = s.schemas[indexID]
	engine := s.engines[s.indexEngine[indexID]]
	s.mu.RUnlock()
	next, ok := viewSchemaExtensionTarget(current, desired)
	if !ok {
		return 0, nil
	}
	extender, ok := engine.(viewindex.SchemaExtender)
	if !ok {
		return 0, nil
	}
	if err := extender.ExtendColumns(ctx, indexID, current, next); err != nil {
		return 0, fmt.Errorf("原地加列：%w", err)
	}
	s.nextIndexRevision(indexID)
	s.mu.Lock()
	s.schemas[indexID] = next
	s.mu.Unlock()
	return next.ViewVersion, nil
}

// viewSchemaExtensionTarget 由当前契约与声明的列算出这次加列后的契约；没有要加的列时 ok 为假。
func viewSchemaExtensionTarget(current viewindex.ViewIndexSchema, desired []*pb.ViewColumn) (viewindex.ViewIndexSchema, bool) {
	if current.ViewVersion == 0 || current.SchemaHash == "" {
		return viewindex.ViewIndexSchema{}, false
	}
	extended, ok := appendViewColumns(current.Columns, desired)
	if !ok {
		return viewindex.ViewIndexSchema{}, false
	}
	next := current
	next.ViewVersion = current.ViewVersion + 1
	next.Columns = extended
	next.SchemaHash = viewindex.HashViewIndexSchema(next)
	return next, true
}

// appendViewColumns 返回已建列加上还没建的声明列（按列名比较，新列按声明顺序排在最后）；没有新列时第二个返回值为假。
// 声明里没有、但已建的列不会被去掉：列只能追加。
func appendViewColumns(active, desired []*pb.ViewColumn) ([]*pb.ViewColumn, bool) {
	built := make(map[string]struct{}, len(active))
	extended := make([]*pb.ViewColumn, 0, len(desired))
	var nextOrder uint32
	for _, column := range active {
		built[column.GetColumnName()] = struct{}{}
		extended = append(extended, column)
		nextOrder = max(nextOrder, column.GetSortOrder()+1)
	}
	found := false
	for _, column := range desired {
		if column == nil || column.GetColumnName() == "" {
			continue
		}
		if _, ok := built[column.GetColumnName()]; ok {
			continue
		}
		clone := proto.Clone(column).(*pb.ViewColumn)
		clone.SortOrder = nextOrder
		nextOrder++
		extended = append(extended, clone)
		found = true
	}
	return extended, found
}
