package scfinvoker

import (
	"context"
	"fmt"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// ActivityEntry 是写入 Admin 全局活动日志的一条记录。
type ActivityEntry struct {
	SpaceID  string
	Source   string
	Category string
	Level    string
	Title    string
	Detail   string
	RefID    string
	DedupKey string
}

// AppendActivityLogs 批量写入活动日志；相同 DedupKey 的重放由 Admin 忽略。
func (c *Client) AppendActivityLogs(ctx context.Context, spaceID string, entries []ActivityEntry) error {
	if len(entries) == 0 {
		return nil
	}
	request := &adminpb.AppendActivityLogsReq{}
	for _, entry := range entries {
		request.Entries = append(request.Entries, &adminpb.ActivityLogEntry{
			SpaceId: entry.SpaceID, Source: entry.Source, Category: entry.Category, Level: entry.Level,
			Title: entry.Title, Detail: entry.Detail, RefId: entry.RefID, DedupKey: entry.DedupKey,
		})
	}
	var rsp adminpb.AppendActivityLogsRsp
	if err := c.invoke(ctx, spaceID, "trpc.moox.admin.ActivityLog", "AppendActivityLogs", request, &rsp, false); err != nil {
		return err
	}
	if rsp.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS {
		return fmt.Errorf("append activity logs: %s", rsp.GetRetInfo().GetMsg())
	}
	return nil
}

// ListActiveSpaceIDs 返回 Admin 中处于启用状态的空间 ID；采集计划对账据此启停各空间的协程。
func (c *Client) ListActiveSpaceIDs(ctx context.Context) (map[string]struct{}, error) {
	active := map[string]struct{}{}
	for page := uint32(1); ; page++ {
		var rsp adminpb.ListSpacesRsp
		request := &adminpb.ListSpacesReq{Status: "active", Page: &adminpb.Page{Page: page, Size: 200}}
		if err := c.invoke(ctx, "", "trpc.moox.admin.SpaceMgr", "ListSpaces", request, &rsp, false); err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list spaces: %s", rsp.GetRetInfo().GetMsg())
		}
		for _, space := range rsp.GetSpaces() {
			active[strings.ToLower(space.GetSpaceId())] = struct{}{}
		}
		if !rsp.GetPageResult().GetHasMore() {
			return active, nil
		}
	}
}
