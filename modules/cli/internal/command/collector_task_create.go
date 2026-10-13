package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/structpb"
)

type collectorTaskCreateFlags struct {
	File        string
	SpaceID     string
	Name        string
	Description string
	TagIDs      []string
	Frequency   string
	Fields      []string
	Symbols     int
	Lateness    int
	Disabled    bool
	DataNodeID  string
}

var collectorTaskCreateFlagsValue collectorTaskCreateFlags

var collectorTaskCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "创建 K 线采集任务（可直接指定 SCF 定时模式与每函数标的数）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runCollectorTaskCreate(cmd.Context(), cmd.OutOrStdout(), collectorTaskCreateFlagsValue)
	},
}

func init() {
	collectorTaskCmd.AddCommand(collectorTaskCreateCmd)
	flags := collectorTaskCreateCmd.Flags()
	flags.StringVar(&collectorTaskCreateFlagsValue.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	flags.StringVar(&collectorTaskCreateFlagsValue.SpaceID, "space-id", "", "空间 ID")
	flags.StringVar(&collectorTaskCreateFlagsValue.Name, "name", "", "任务名称")
	flags.StringVar(&collectorTaskCreateFlagsValue.Description, "description", "", "任务说明")
	flags.StringSliceVar(&collectorTaskCreateFlagsValue.TagIDs, "tag-id", nil, "标的标签，可重复")
	flags.StringVar(&collectorTaskCreateFlagsValue.Frequency, "frequency", "1m", "K 线频率")
	flags.StringSliceVar(&collectorTaskCreateFlagsValue.Fields, "output-field", []string{"close", "high", "low", "open", "quote_volume", "trade_num", "volume"}, "输出字段")
	flags.IntVar(&collectorTaskCreateFlagsValue.Symbols, "symbols-per-function", 0, "定时模式下每个函数负责的标的数（0 为默认 50）")
	flags.IntVar(&collectorTaskCreateFlagsValue.Lateness, "allowed-lateness-seconds", 0, "结果数据集的允许迟到时间（0 为空间默认）")
	flags.BoolVar(&collectorTaskCreateFlagsValue.Disabled, "disabled", false, "创建后保持停用")
	flags.StringVar(&collectorTaskCreateFlagsValue.DataNodeID, "data-node-id", "", "结果数据节点；留空使用默认")
}

func runCollectorTaskCreate(ctx context.Context, out io.Writer, flags collectorTaskCreateFlags) error {
	if strings.TrimSpace(flags.SpaceID) == "" || strings.TrimSpace(flags.Name) == "" || len(flags.TagIDs) == 0 {
		return errors.New("--space-id, --name and at least one --tag-id are required")
	}
	params := map[string]any{
		"subject_tags": stringsToAny(flags.TagIDs), "frequency": flags.Frequency, "output_fields": stringsToAny(flags.Fields),
	}
	if flags.Symbols > 0 {
		params["symbols_per_function"] = flags.Symbols
	}
	if flags.Lateness > 0 {
		params["allowed_lateness_seconds"] = flags.Lateness
	}
	structured, err := structpb.NewStruct(params)
	if err != nil {
		return err
	}
	enabled := !flags.Disabled
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &collectorpb.CreateTaskRsp{}
	req := &collectorpb.CreateTaskReq{
		Task:         &collectorpb.CollectionTask{SpaceId: flags.SpaceID, TaskName: flags.Name, Description: flags.Description, DataType: "kline", TagIds: flags.TagIDs, CollectParams: structured, Enabled: &enabled, Creator: "moox-cli"},
		ResultConfig: &collectorpb.ResultConfig{DataNodeId: flags.DataNodeID},
	}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "CreateTask", req, rsp); err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	if err := collectorRetError("create task", rsp.GetRetInfo()); err != nil {
		return err
	}
	return printJSON(out, map[string]any{"status": "created", "task_id": rsp.GetTaskId()})
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

var collectorTaskDeleteFlagsValue struct {
	KeepResult bool
	File       string
	SpaceID    string
	TaskID     string
}

var collectorTaskDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "删除采集任务及其结果",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		flags := collectorTaskDeleteFlagsValue
		if flags.SpaceID == "" || flags.TaskID == "" {
			return errors.New("--space-id and --task-id are required")
		}
		gateway, err := openCommandGateway(cmd.Context(), flags.File, nil)
		if err != nil {
			return err
		}
		defer gateway.Close()
		rsp := &collectorpb.DeleteTaskRsp{}
		if err := gateway.Invoke(cmd.Context(), "trpc.moox.collector.CollectMgr", "DeleteTask", &collectorpb.DeleteTaskReq{SpaceId: flags.SpaceID, TaskId: flags.TaskID, DeleteResultData: !flags.KeepResult}, rsp); err != nil {
			return fmt.Errorf("delete task: %w", err)
		}
		if err := collectorRetError("delete task", rsp.GetRetInfo()); err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), map[string]any{"status": "deleted", "task_id": flags.TaskID})
	},
}

func init() {
	collectorTaskCmd.AddCommand(collectorTaskDeleteCmd)
	flags := collectorTaskDeleteCmd.Flags()
	flags.StringVar(&collectorTaskDeleteFlagsValue.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	flags.StringVar(&collectorTaskDeleteFlagsValue.SpaceID, "space-id", "", "空间 ID")
	flags.StringVar(&collectorTaskDeleteFlagsValue.TaskID, "task-id", "", "任务 ID")
	flags.BoolVar(&collectorTaskDeleteFlagsValue.KeepResult, "keep-result", false, "只删任务，保留结果数据集与 View")
}

var collectorResultDeleteFlagsValue struct {
	File, SpaceID, AuthFile string
	DatasetIDs, ViewIDs     []string
}

// collectorResultDeleteCmd 删除已没有任务引用的采集结果（View、行数据、数据集）。
var collectorResultDeleteCmd = &cobra.Command{
	Use:   "delete-result",
	Short: "删除没有任务引用的采集结果（View、行数据与数据集）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		flags := collectorResultDeleteFlagsValue
		if flags.SpaceID == "" || (len(flags.DatasetIDs) == 0 && len(flags.ViewIDs) == 0) {
			return errors.New("--space-id and at least one --dataset-id/--view-id are required")
		}
		metadata, closer, err := newCollectorStorageMetadataClient(cmd.Context(), flags.File, nil, flags.AuthFile)
		if err != nil {
			return err
		}
		defer closer.Close()
		summary := &collectorTaskPurgeSummary{}
		for _, id := range flags.ViewIDs {
			summary.storageViewRefs = appendUniqueStorageObject(summary.storageViewRefs, flags.SpaceID, id)
		}
		for _, id := range flags.DatasetIDs {
			summary.storageDatasetRefs = appendUniqueStorageObject(summary.storageDatasetRefs, flags.SpaceID, id)
		}
		if err := deleteCollectorOwnedMetadata(cmd.Context(), metadata, summary); err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), map[string]any{"status": "deleted", "views": flags.ViewIDs, "datasets": flags.DatasetIDs})
	},
}

func init() {
	collectorTaskCmd.AddCommand(collectorResultDeleteCmd)
	flags := collectorResultDeleteCmd.Flags()
	flags.StringVar(&collectorResultDeleteFlagsValue.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	flags.StringVar(&collectorResultDeleteFlagsValue.AuthFile, "storage-auth-file", "secrets/storage-internal-auth.env", "Storage 内部鉴权文件")
	flags.StringVar(&collectorResultDeleteFlagsValue.SpaceID, "space-id", "", "空间 ID")
	flags.StringArrayVar(&collectorResultDeleteFlagsValue.DatasetIDs, "dataset-id", nil, "要删除的数据集（连同行数据），可重复")
	flags.StringArrayVar(&collectorResultDeleteFlagsValue.ViewIDs, "view-id", nil, "要删除的 View，可重复；先于数据集删除")
}
