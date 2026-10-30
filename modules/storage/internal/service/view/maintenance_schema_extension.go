package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// extendViewSchemasRound 给声明了新列的 View 原地加列，由每分钟的定时任务触发。新列默认为空，不回填。
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
			log.Printf("storage view 检查加列时读取 View 失败：%v", err)
			return
		}
		for _, view := range rsp.GetViews() {
			if ctx.Err() != nil {
				return
			}
			if view == nil || view.GetActiveIndexId() == "" {
				continue
			}
			engine := s.engines[strings.ToLower(strings.TrimSpace(view.GetEngine()))]
			extended, err := s.extendViewSchema(ctx, metadata, auth, view, engine, false)
			if err != nil {
				log.Printf("storage view 加列失败 space=%s view=%s：%v", view.GetSpaceId(), view.GetViewId(), err)
			} else if extended {
				log.Printf("storage view 加列完成 space=%s view=%s 修订号=%d", view.GetSpaceId(), view.GetViewId(), view.GetViewRevision()+1)
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			return
		}
	}
}

// recoverViewSchemaExtension 处理索引已经加列、元数据提交却没有完成的窗口（进程在两步之间退出）：物理修订号比元数据大 1，
// 重新走一遍加列；ExtendColumns 负责校验物理状态，并接着追加这期间新声明的列。
func (s *Service) recoverViewSchemaExtension(ctx context.Context, metadata MetadataClient, auth *pb.AuthInfo, view *pb.View, engine viewindex.Engine, stats viewindex.ViewIndexStats) (bool, error) {
	if !stats.Exists || stats.ViewVersion != view.GetViewRevision()+1 {
		return false, nil
	}
	recovered, err := s.extendViewSchema(ctx, metadata, auth, view, engine, true)
	if errors.Is(err, viewindex.ErrSchemaExtensionConflict) {
		return false, nil
	}
	return recovered, err
}

func (s *Service) extendViewSchema(ctx context.Context, metadata MetadataClient, auth *pb.AuthInfo, view *pb.View, engine viewindex.Engine, allowDetached bool) (bool, error) {
	current, next, extender, ok, err := viewSchemaExtensionTarget(ctx, metadata, auth, view, engine)
	if err != nil || !ok {
		return false, err
	}
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.Lock()
	runtime := s.views[viewKey]
	if runtime == nil {
		if !allowDetached {
			s.mu.Unlock()
			return false, nil
		}
		runtime = newViewRuntime()
		s.views[viewKey] = runtime
	}
	s.mu.Unlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !allowDetached && runtime.active != view.GetActiveIndexId() {
		return false, nil
	}
	release, err := s.indexWriteGate(view.GetActiveIndexId()).lock(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	if err := extender.ExtendColumns(ctx, view.GetActiveIndexId(), current, next); err != nil {
		return false, fmt.Errorf("原地加列：%w", err)
	}
	s.nextIndexRevision(view.GetActiveIndexId())
	rsp, err := metadata.CommitViewSchemaExtension(ctx, &pb.CommitViewSchemaExtensionReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), ActiveIndexId: view.GetActiveIndexId(),
		ExpectedViewRevision: view.GetViewRevision(), ExpectedSchemaHash: view.GetActiveViewSchemaHash(),
		Columns: next.Columns, ViewSchemaHash: next.SchemaHash,
	})
	if err == nil {
		err = requireSuccess(rsp.GetRetInfo())
	}
	if err != nil {
		return false, fmt.Errorf("提交加列：%w", err)
	}
	committed := rsp.GetView()
	if committed.GetActiveIndexId() != view.GetActiveIndexId() || committed.GetViewRevision() != next.ViewVersion ||
		committed.GetActiveViewSchemaHash() != next.SchemaHash ||
		!proto.Equal(&pb.View{ActiveColumns: committed.GetActiveColumns()}, &pb.View{ActiveColumns: next.Columns}) {
		return false, errors.New("元数据返回的已建列与这次加列不一致")
	}
	s.attachActiveViewLocked(committed, runtime, next, next.Columns, next.Engine)
	return true, nil
}

// viewSchemaExtensionTarget 算出这次加列的前后契约；没有要加的列时 ok 为假。
func viewSchemaExtensionTarget(ctx context.Context, metadata MetadataClient, auth *pb.AuthInfo, view *pb.View, engine viewindex.Engine) (viewindex.ViewIndexSchema, viewindex.ViewIndexSchema, viewindex.SchemaExtender, bool, error) {
	var none viewindex.ViewIndexSchema
	if view == nil || engine == nil || view.GetActiveIndexId() == "" || view.GetViewRevision() == 0 {
		return none, none, nil, false, nil
	}
	extender, ok := engine.(viewindex.SchemaExtender)
	if !ok {
		return none, none, nil, false, nil
	}
	desired, err := desiredViewColumns(ctx, metadata, auth, view)
	if err != nil {
		return none, none, nil, false, err
	}
	extended, ok := appendViewColumns(view.GetActiveColumns(), desired)
	if !ok {
		return none, none, nil, false, nil
	}
	current := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), PrimaryDatasetID: view.GetDatasetId(),
		ViewVersion: view.GetViewRevision(), Engine: engine.Engine(), Columns: view.GetActiveColumns(),
		SchemaHash: view.GetActiveViewSchemaHash(),
	}
	next := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), PrimaryDatasetID: view.GetDatasetId(),
		ViewVersion: view.GetViewRevision() + 1, Engine: engine.Engine(), Columns: extended,
	}
	next.SchemaHash = viewindex.HashViewIndexSchema(next)
	return current, next, extender, true, nil
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
