// Package adminrpc 是 Collector 经网关调 Admin 写全局活动日志。
package adminrpc

import (
	"context"
	"errors"
	"fmt"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"google.golang.org/protobuf/proto"
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

// Client 借用进程的网关客户端调 Admin。
type Client struct {
	gateway gatewayclient.Invoker
}

// New 用进程的网关客户端构造 Admin 客户端。
func New(gateway gatewayclient.Invoker) *Client {
	return &Client{gateway: gateway}
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
	if err := c.invoke(ctx, spaceID, "trpc.moox.admin.ActivityLog", "AppendActivityLogs", request, &rsp); err != nil {
		return err
	}
	if rsp.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS {
		return fmt.Errorf("append activity logs: %s", rsp.GetRetInfo().GetMsg())
	}
	return nil
}

func (c *Client) invoke(ctx context.Context, spaceID, service, method string, request, response proto.Message) error {
	if c == nil || c.gateway == nil {
		return errors.New("调 Admin 需要进程的网关客户端")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	metadata.SpaceID = spaceID
	return c.gateway.Invoke(gatewayclient.WithCallMetadata(ctx, metadata), service, method, request, response)
}
