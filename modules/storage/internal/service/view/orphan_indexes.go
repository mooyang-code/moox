package view

import (
	"context"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"

	"trpc.group/trpc-go/trpc-go/log"
)

// managedIndexRef 是某个引擎下的一个索引文件。
type managedIndexRef struct {
	engine  string
	indexID string
}

// removeOrphanIndexes 在启动时删掉没有对应数据集的索引文件：数据集在进程停着的时候被删（例如离线的 delete-view），
// 或运行中摘除时文件没删掉。元数据里的每个数据集（含停用的）都保留它所用引擎下的索引；specs 必须是完整列出的全部数据集。
// 只在挂接索引之前调用：这时没有运行时、跟随器和查询在用这些文件。失败只记日志，不挡启动。
func (s *Service) removeOrphanIndexes(ctx context.Context, specs []*viewSpec) {
	owned := make(map[managedIndexRef]struct{}, len(specs))
	for _, spec := range specs {
		owned[managedIndexRef{engine: spec.engineName(), indexID: viewindex.IndexID(spec.SpaceID, spec.DatasetID)}] = struct{}{}
	}
	names := make([]string, 0, len(s.engines))
	for name := range s.engines {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		engine := s.engines[name]
		lister, ok := engine.(viewindex.ManagedIndexLister)
		if !ok {
			continue
		}
		ids, err := lister.ListManagedIndexes(ctx)
		if err != nil {
			log.Warnf("storage view 启动时列出 %s 索引文件失败，不清理这个引擎的孤立文件：%v", name, err)
			continue
		}
		for _, id := range ids {
			if _, ok := owned[managedIndexRef{engine: normalizedEngine(name), indexID: id}]; ok {
				continue
			}
			if err := engine.Remove(ctx, id); err != nil {
				log.Errorf("storage view 删除孤立的索引文件失败 engine=%s index_id=%s：%v", name, id, err)
				continue
			}
			log.Infof("storage view 已删除孤立的索引文件 engine=%s index_id=%s", name, id)
		}
	}
}

func normalizedEngine(engine string) string {
	return strings.ToLower(strings.TrimSpace(engine))
}
