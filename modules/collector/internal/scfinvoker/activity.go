package scfinvoker

import (
	"context"
	"fmt"

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
