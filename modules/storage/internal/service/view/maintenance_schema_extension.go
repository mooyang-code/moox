package view

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"

	"trpc.group/trpc-go/trpc-go/log"
)

// extendViewSchemasRound 给新加了列的数据集的 View 原地加列，由每分钟的定时任务触发。新列默认为空，不回填。
func (s *Service) extendViewSchemasRound(ctx context.Context) {
	metadata := s.metadataClientSnapshot()
	if metadata == nil {
		return
	}
	specs, err := listViewSpecs(ctx, metadata)
	if err != nil {
		log.Errorf("storage view 检查加列时读取数据集失败：%v", err)
		return
	}
	for _, spec := range specs {
		if ctx.Err() != nil {
			return
		}
		if spec.Status != "active" {
			continue
		}
		revision, err := s.extendViewSchema(ctx, spec)
		if err != nil {
			log.Errorf("storage view 加列失败 space=%s dataset=%s：%v", spec.SpaceID, spec.DatasetID, err)
		} else if revision > 0 {
			log.Infof("storage view 加列完成 space=%s dataset=%s 修订号=%d", spec.SpaceID, spec.DatasetID, revision)
		}
	}
}

// extendViewSchema 把数据集里还没建进活动索引的启用列原地加进去，返回加列后的修订号；没有要加的列时返回 0。
// 当前契约取内存里活动索引的契约（与索引文件一致），加列和新契约在索引的一次提交里落盘。
func (s *Service) extendViewSchema(ctx context.Context, spec *viewSpec) (uint64, error) {
	runtime := s.runtimeOf(spec.ref())
	if runtime == nil {
		return 0, nil
	}
	// 先按原子副本判断有没有要加的列：每分钟都要检查所有 View，绝大多数没有新列，不用等运行时锁。
	state := runtime.queryState()
	if !state.active() {
		return 0, nil
	}
	if _, ok := viewSchemaExtensionTarget(state.schema, spec.Columns); !ok {
		return 0, nil
	}
	extender, ok := runtime.engine.(viewindex.SchemaExtender)
	if !ok {
		return 0, nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.phase.isActive() {
		return 0, nil
	}
	release, err := runtime.gate.lock(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	// 定时任务和建索引都在这两把锁下改契约：持锁后重取一次，用过期的快照算目标会把刚提交的修订号再加一遍列。
	current := runtime.schema
	next, ok := viewSchemaExtensionTarget(current, spec.Columns)
	if !ok {
		return 0, nil
	}
	endWrite := runtime.beginEngineWrite()
	defer endWrite()
	if err := extender.ExtendColumns(ctx, runtime.indexID, current, next); err != nil {
		return 0, fmt.Errorf("原地加列：%w", err)
	}
	runtime.schema = next
	runtime.publishReadStateLocked()
	return next.ViewVersion, nil
}

// viewSchemaExtensionTarget 由当前契约与数据集启用的列算出这次加列后的契约；没有要加的列时 ok 为假。
func viewSchemaExtensionTarget(current viewindex.ViewIndexSchema, desired []*viewindex.Column) (viewindex.ViewIndexSchema, bool) {
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

// appendViewColumns 返回已建列加上还没建的列（按列名比较，新列按建列顺序排在最后）；没有新列时第二个返回值为假。
// 数据集里停用、但已建的列不会被去掉：列只能追加，停用的列只在返回结果时隐藏。
func appendViewColumns(active, desired []*viewindex.Column) ([]*viewindex.Column, bool) {
	built := make(map[string]struct{}, len(active))
	extended := make([]*viewindex.Column, 0, len(desired))
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
		extended = append(extended, &viewindex.Column{ColumnName: column.ColumnName, ValueType: column.ValueType, SortOrder: nextOrder})
		nextOrder++
		found = true
	}
	return extended, found
}
