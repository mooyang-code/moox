// Package archivetasks 经主机网关只读查询 archive-mgr 的归档任务：删除策略实例前确认结果数据集没有归档任务。
package archivetasks

import (
	"context"
	"errors"
	"fmt"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

const (
	archiveMgrService = "trpc.moox.archive.ArchiveMgr"
	// pageSize 是 archive-mgr 接受的最大每页条数。
	pageSize = 200
	// maxPages 限制翻页次数：一个空间的归档任务按数据集一个，远少于 maxPages × pageSize。
	maxPages = 100
)

// Client 经网关调用 archive-mgr 的 ListArchiveTasks（只读）。超时由调用方 ctx 的截止时间与服务目录的路由超时决定。
type Client struct {
	Gateway gatewayclient.Invoker
}

// ArchivedDatasets 分页拉回空间内的全部归档任务，返回有归档任务的数据集 ID。
// archive-mgr 不能按数据集过滤，只能在本地筛选。
func (c *Client) ArchivedDatasets(ctx context.Context, spaceID string) (map[string]bool, error) {
	if c == nil || c.Gateway == nil {
		return nil, errors.New("strategy 网关客户端未配置，不能查询归档任务")
	}
	datasets := map[string]bool{}
	for page := uint32(1); page <= maxPages; page++ {
		rsp := &archivepb.ListArchiveTasksRsp{}
		req := &archivepb.ListArchiveTasksReq{SpaceId: spaceID, Page: &commonpb.Page{Page: page, Size: pageSize}}
		if err := c.Gateway.Invoke(ctx, archiveMgrService, "ListArchiveTasks", req, rsp); err != nil {
			return nil, fmt.Errorf("查询归档任务失败：%w", err)
		}
		if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("查询归档任务失败：%s（%s）", rsp.GetRetInfo().GetMsg(), code.String())
		}
		for _, task := range rsp.GetTasks() {
			if task.GetSpaceId() == "" || task.GetSpaceId() == spaceID {
				datasets[task.GetDatasetId()] = true
			}
		}
		// 服务端会把每页条数夹到上限，结束与否只看 has_more。
		if !rsp.GetPageResult().GetHasMore() || len(rsp.GetTasks()) == 0 {
			return datasets, nil
		}
	}
	return nil, fmt.Errorf("查询归档任务失败：超过 %d 页仍没有取完", maxPages)
}
