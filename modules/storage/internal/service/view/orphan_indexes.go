package view

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"

	"trpc.group/trpc-go/trpc-go/log"
)

// managedIndexRef 是某个引擎下的一个索引文件。
type managedIndexRef struct {
	engine  string
	indexID string
}

// removeOrphanIndexes 在启动时删掉没有对应 View 的索引文件：View 在进程停着的时候被删（例如离线的 delete-view），
// 或运行中摘除时文件没删掉。元数据里的每个 View（含停用的）都保留它所用引擎下的索引；元数据列不全时一个都不删。
// 只在挂接索引之前调用：这时没有运行时、跟随器和查询在用这些文件。失败只记日志，不挡启动。
func (s *Service) removeOrphanIndexes(ctx context.Context, metadata MetadataClient) {
	owned, err := metadataIndexes(ctx, metadata)
	if err != nil {
		log.Warnf("storage view 启动时列出全部 View 失败，不清理孤立的索引文件：%v", err)
		return
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

// metadataIndexes 列出元数据里全部 View（含停用的）各自的索引。分页信息不完整时不能断定已经列全，返回错误。
func metadataIndexes(ctx context.Context, metadata MetadataClient) (map[managedIndexRef]struct{}, error) {
	owned := make(map[managedIndexRef]struct{})
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadata.ListViews(ctx, &pb.ListViewsReq{Page: &pb.Page{Page: pageNo, Size: 100}})
		if err != nil {
			return nil, err
		}
		if rsp == nil {
			return nil, errors.New("列出 View 返回了空响应")
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, view := range rsp.GetViews() {
			if view != nil && view.GetSpaceId() != "" && view.GetViewId() != "" {
				owned[managedIndexRef{engine: normalizedEngine(view.GetEngine()), indexID: viewindex.ViewIndexID(view.GetSpaceId(), view.GetViewId())}] = struct{}{}
			}
		}
		pageResult := rsp.GetPageResult()
		if pageResult == nil {
			return nil, errors.New("列出 View 没有返回分页信息")
		}
		if pageResult.GetHasMore() && len(rsp.GetViews()) == 0 {
			return nil, errors.New("列出 View 时这一页为空却说还有下一页")
		}
		if !pageResult.GetHasMore() {
			return owned, nil
		}
	}
}

func normalizedEngine(engine string) string {
	return strings.ToLower(strings.TrimSpace(engine))
}
