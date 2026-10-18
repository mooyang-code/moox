package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/spf13/cobra"
)

type collectorSubjectsSyncFlags struct {
	File       string
	SpaceID    string
	TagID      string
	Attributes bool
}

var collectorSubjectsSyncFlagsValue collectorSubjectsSyncFlags

var collectorSubjectsCmd = &cobra.Command{
	Use:   "subjects",
	Short: "标的同步工具",
}

var collectorSubjectsSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "立即同步币安/东财的标的标签成员，不等整点定时任务",
	Long: "首次部署、迁移或交易所接口恢复后，用它强制同步一次标的标签成员（--attributes 同时刷新标的属性）。\n" +
		"无视各标签的 cron 窗口；只处理 mode=auto 且配置了数据源的标签。输出 JSON：每个标签的状态、标的数和失败原因；" +
		"任一标签失败时命令以非零状态退出。同步正在运行时返回冲突，稍后重试即可。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runCollectorSubjectsSync(cmd.Context(), cmd.OutOrStdout(), collectorSubjectsSyncFlagsValue)
	},
}

func init() {
	collectorCmd.AddCommand(collectorSubjectsCmd)
	collectorSubjectsCmd.AddCommand(collectorSubjectsSyncCmd)
	flags := collectorSubjectsSyncCmd.Flags()
	flags.StringVar(&collectorSubjectsSyncFlagsValue.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	flags.StringVar(&collectorSubjectsSyncFlagsValue.SpaceID, "space-id", "", "只同步指定空间；留空表示全部空间")
	flags.StringVar(&collectorSubjectsSyncFlagsValue.TagID, "tag-id", "", "只同步指定标签；必须同时指定 --space-id")
	flags.BoolVar(&collectorSubjectsSyncFlagsValue.Attributes, "attributes", false, "同时刷新标的属性（忽略定时窗口）")
}

type collectorSubjectsSyncOutput struct {
	Status     string                       `json:"status"`
	Tags       []collectorSubjectTagResult  `json:"tags"`
	Attributes []collectorSubjectAttrResult `json:"attributes,omitempty"`
}

type collectorSubjectTagResult struct {
	SpaceID  string `json:"space_id"`
	TagID    string `json:"tag_id"`
	Status   string `json:"status"`
	Subjects uint32 `json:"subjects"`
	Message  string `json:"message,omitempty"`
}

type collectorSubjectAttrResult struct {
	SpaceID string `json:"space_id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func runCollectorSubjectsSync(ctx context.Context, out io.Writer, flags collectorSubjectsSyncFlags) error {
	spaceID, tagID := strings.TrimSpace(flags.SpaceID), strings.TrimSpace(flags.TagID)
	if tagID != "" && spaceID == "" {
		return errors.New("--tag-id requires --space-id")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &collectorpb.SyncSubjectsRsp{}
	req := &collectorpb.SyncSubjectsReq{SpaceId: spaceID, TagId: tagID, IncludeAttributes: flags.Attributes}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "SyncSubjects", req, rsp); err != nil {
		return fmt.Errorf("sync subjects: %w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != collectorpb.ErrorCode_SUCCESS {
		return fmt.Errorf("sync subjects: %s (code %d)", rsp.GetRetInfo().GetMsg(), code)
	}
	result := collectorSubjectsSyncOutput{Status: "success", Tags: []collectorSubjectTagResult{}}
	failed := 0
	for _, tag := range rsp.GetTags() {
		result.Tags = append(result.Tags, collectorSubjectTagResult{SpaceID: tag.GetSpaceId(), TagID: tag.GetTagId(), Status: tag.GetStatus(), Subjects: tag.GetSubjects(), Message: tag.GetMessage()})
		if tag.GetStatus() == "failed" {
			failed++
		}
	}
	for _, attribute := range rsp.GetAttributes() {
		result.Attributes = append(result.Attributes, collectorSubjectAttrResult{SpaceID: attribute.GetSpaceId(), Status: attribute.GetStatus(), Message: attribute.GetMessage()})
		if attribute.GetStatus() == "failed" {
			failed++
		}
	}
	if failed > 0 {
		result.Status = "failed"
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d tag or attribute sync(s) failed", failed)
	}
	return nil
}
