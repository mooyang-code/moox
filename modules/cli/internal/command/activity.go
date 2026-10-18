package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/spf13/cobra"
)

type activityListFlags struct {
	File     string
	SpaceID  string
	Source   string
	Category string
	RefID    string
	Since    string
	Until    string
	Page     uint32
	Size     uint32
}

var activityListFlagsValue activityListFlags

var activityCmd = &cobra.Command{Use: "activity", Short: "全局活动日志"}

var activityListCmd = &cobra.Command{
	Use:   "list",
	Short: "查看活动日志（如采集标的变更），按时间倒序",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runActivityList(cmd.Context(), cmd.OutOrStdout(), activityListFlagsValue)
	},
}

func init() {
	rootCmd.AddCommand(activityCmd)
	activityCmd.AddCommand(activityListCmd)
	flags := activityListCmd.Flags()
	flags.StringVar(&activityListFlagsValue.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	flags.StringVar(&activityListFlagsValue.SpaceID, "space-id", "", "只看指定空间；留空表示全部")
	flags.StringVar(&activityListFlagsValue.Source, "source", "", "只看指定来源模块")
	flags.StringVar(&activityListFlagsValue.Category, "category", "", "只看指定类别，如 collector.subject_change")
	flags.StringVar(&activityListFlagsValue.RefID, "ref-id", "", "只看指定关联对象")
	flags.StringVar(&activityListFlagsValue.Since, "since", "", "起始时间（含），RFC3339")
	flags.StringVar(&activityListFlagsValue.Until, "until", "", "结束时间（不含），RFC3339")
	flags.Uint32Var(&activityListFlagsValue.Page, "page", 1, "页号")
	flags.Uint32Var(&activityListFlagsValue.Size, "size", 50, "每页条数（最多 200）")
}

func runActivityList(ctx context.Context, out io.Writer, flags activityListFlags) error {
	since, until := strings.TrimSpace(flags.Since), strings.TrimSpace(flags.Until)
	var err error
	if since != "" {
		if since, err = parseKlineTime("since", since); err != nil {
			return err
		}
	}
	if until != "" {
		if until, err = parseKlineTime("until", until); err != nil {
			return err
		}
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &adminpb.ListActivityLogsRsp{}
	req := &adminpb.ListActivityLogsReq{SpaceId: flags.SpaceID, Source: flags.Source, Category: flags.Category, RefId: flags.RefID, Since: since, Until: until, Page: &adminpb.Page{Page: flags.Page, Size: flags.Size}}
	if err := gateway.Invoke(ctx, "trpc.moox.admin.ActivityLog", "ListActivityLogs", req, rsp); err != nil {
		return fmt.Errorf("list activity logs: %w", err)
	}
	if rsp.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS {
		return errors.New("list activity logs: " + rsp.GetRetInfo().GetMsg())
	}
	items := make([]map[string]any, 0, len(rsp.GetItems()))
	for _, item := range rsp.GetItems() {
		items = append(items, map[string]any{
			"id": item.GetId(), "time": item.GetCreatedAt(), "space_id": item.GetSpaceId(), "source": item.GetSource(), "category": item.GetCategory(),
			"level": item.GetLevel(), "title": item.GetTitle(), "ref_id": item.GetRefId(), "detail": jsonRawOrString(item.GetDetail()),
		})
	}
	return printJSON(out, map[string]any{"items": items, "total": rsp.GetPage().GetTotal(), "has_more": rsp.GetPage().GetHasMore()})
}

func jsonRawOrString(value string) any {
	var parsed any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return value
	}
	return parsed
}
