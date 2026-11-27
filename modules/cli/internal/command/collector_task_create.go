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
	// AllFields 为真时不指定 output_fields：输出该品种行的全部字段，与内置任务不写 output_fields 相同。
	AllFields bool
	Symbols   int
	Lateness  int
	Disabled  bool
	Ingest    string
	// SourceTaskID 不为空时建 K 线合成任务：源参数由服务端按源任务推导，--frequency 是目标周期。
	SourceTaskID string
}

var collectorTaskCreateFlagsValue collectorTaskCreateFlags

var collectorTaskCmd = &cobra.Command{
	Use:   "task",
	Short: "采集任务维护工具",
}

var collectorTaskCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "创建 K 线采集任务或合成任务（--source-task-id）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(collectorTaskCreateFlagsValue.SourceTaskID) != "" {
			if err := validateResampleCreateFlags(cmd); err != nil {
				return err
			}
			return runCollectorTaskCreate(cmd.Context(), cmd.OutOrStdout(), collectorTaskCreateFlagsValue, cmd.Flags().Changed("output-field"))
		}
		if collectorTaskCreateFlagsValue.AllFields && cmd.Flags().Changed("output-field") {
			return errors.New("--all-output-fields 不能与 --output-field 同时用")
		}
		return runCollectorTaskCreate(cmd.Context(), cmd.OutOrStdout(), collectorTaskCreateFlagsValue, cmd.Flags().Changed("output-field"))
	},
}

// validateResampleCreateFlags 检查合成任务的参数：目标周期必须显式给出；标的、采集方式、每函数标的数和迟到时间都跟源任务或固定值走，不能指定。
func validateResampleCreateFlags(cmd *cobra.Command) error {
	if !cmd.Flags().Changed("frequency") {
		return errors.New("合成任务必须用 --frequency 指定目标周期（例如 5m）")
	}
	for _, name := range []string{"tag-id", "ingest", "symbols-per-function", "allowed-lateness-seconds", "all-output-fields"} {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("合成任务不能指定 --%s：标的与源参数由源任务决定", name)
		}
	}
	return nil
}

func init() {
	collectorCmd.AddCommand(collectorTaskCmd)
	collectorTaskCmd.AddCommand(collectorTaskCreateCmd)
	flags := collectorTaskCreateCmd.Flags()
	flags.StringVar(&collectorTaskCreateFlagsValue.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	flags.StringVar(&collectorTaskCreateFlagsValue.SpaceID, "space-id", "", "空间 ID")
	flags.StringVar(&collectorTaskCreateFlagsValue.Name, "name", "", "任务名称")
	flags.StringVar(&collectorTaskCreateFlagsValue.Description, "description", "", "任务说明")
	flags.StringSliceVar(&collectorTaskCreateFlagsValue.TagIDs, "tag-id", nil, "标的标签，可重复")
	flags.StringVar(&collectorTaskCreateFlagsValue.Frequency, "frequency", "1m", "K 线频率；合成任务是目标周期，必须显式给出")
	flags.StringVar(&collectorTaskCreateFlagsValue.SourceTaskID, "source-task-id", "", "建 K 线合成任务：源采集任务 ID（源数据集、源周期由服务端推导；先用 collector resample preview 看目标周期是否可行）")
	flags.StringSliceVar(&collectorTaskCreateFlagsValue.Fields, "output-field", nil, "输出字段（可重复）；不给时由服务端按市场或源数据集输出全部默认可采集/可合成列")
	flags.BoolVar(&collectorTaskCreateFlagsValue.AllFields, "all-output-fields", false, "显式输出该品种行的全部默认字段（不传 --output-field 时默认亦如此）；不能与 --output-field 同时用")
	flags.IntVar(&collectorTaskCreateFlagsValue.Symbols, "symbols-per-function", 0, "定时模式下每个函数负责的标的数（0 为默认 50）")
	flags.IntVar(&collectorTaskCreateFlagsValue.Lateness, "allowed-lateness-seconds", 0, "结果数据集的允许迟到时间（0 为空间默认）")
	flags.BoolVar(&collectorTaskCreateFlagsValue.Disabled, "disabled", false, "创建后保持停用")
	flags.StringVar(&collectorTaskCreateFlagsValue.Ingest, "ingest", "", "采集方式：stream（collector-stream 接收推送）、scf（云函数定时拉取）或 local（collector-stream 本地定时拉取）；留空用市场默认值")
}

// fieldsGiven 表示调用方是否显式指定了 --output-field；未指定时不发 output_fields，由服务端按市场或源数据集列规则全量输出。
func runCollectorTaskCreate(ctx context.Context, out io.Writer, flags collectorTaskCreateFlags, fieldsGiven bool) error {
	resample := strings.TrimSpace(flags.SourceTaskID) != ""
	if strings.TrimSpace(flags.SpaceID) == "" || strings.TrimSpace(flags.Name) == "" {
		return errors.New("--space-id and --name are required")
	}
	if !resample && len(flags.TagIDs) == 0 {
		return errors.New("at least one --tag-id is required（建合成任务用 --source-task-id）")
	}
	dataType, params := "kline", collectorTaskCreateParams(flags)
	if resample {
		dataType, params = "kline_resample", collectorResampleTaskParams(flags, fieldsGiven)
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
		Task: &collectorpb.CollectionTask{SpaceId: flags.SpaceID, TaskName: flags.Name, Description: flags.Description, DataType: dataType, Ingest: strings.ToLower(strings.TrimSpace(flags.Ingest)), TagIds: flags.TagIDs, CollectParams: structured, Enabled: &enabled, Creator: "moox-cli"},
	}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "CreateTask", req, rsp); err != nil {
		return fmt.Errorf("create task: %w", err)
	}
	if err := collectorRetError("create task", rsp.GetRetInfo()); err != nil {
		return err
	}
	return printJSON(out, map[string]any{"status": "created", "task_id": rsp.GetTaskId()})
}

// collectorTaskCreateParams 拼出建任务的采集参数。未显式指定 Fields 或 AllFields 为真时不带 output_fields。
func collectorTaskCreateParams(flags collectorTaskCreateFlags) map[string]any {
	params := map[string]any{"subject_tags": stringsToAny(flags.TagIDs), "frequency": flags.Frequency}
	if !flags.AllFields && len(flags.Fields) > 0 {
		params["output_fields"] = stringsToAny(flags.Fields)
	}
	if flags.Symbols > 0 {
		params["symbols_per_function"] = flags.Symbols
	}
	if flags.Lateness > 0 {
		params["allowed_lateness_seconds"] = flags.Lateness
	}
	return params
}

// collectorResampleTaskParams 拼出合成任务的参数，与管理台一致：只给源任务和目标周期，源数据集、源周期、序列由服务端推导。
func collectorResampleTaskParams(flags collectorTaskCreateFlags, fieldsGiven bool) map[string]any {
	params := map[string]any{"source_task_id": strings.TrimSpace(flags.SourceTaskID), "target_frequency": strings.TrimSpace(flags.Frequency)}
	if fieldsGiven {
		params["output_fields"] = stringsToAny(flags.Fields)
	}
	return params
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
	Yes        bool
	File       string
	SpaceID    string
	TaskID     string
}

var collectorTaskDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "删除采集任务及其结果（默认只预览，--yes 才执行）",
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
		if !flags.Yes {
			detail := &collectorpb.GetTaskDetailRsp{}
			if err := gateway.Invoke(cmd.Context(), "trpc.moox.collector.CollectMgr", "GetTaskDetail", &collectorpb.GetTaskDetailReq{SpaceId: flags.SpaceID, TaskId: flags.TaskID}, detail); err != nil {
				return fmt.Errorf("get task: %w", err)
			}
			if err := collectorRetError("get task", detail.GetRetInfo()); err != nil {
				return err
			}
			return writePreview(cmd, map[string]any{
				"action":             "delete_task",
				"space_id":           flags.SpaceID,
				"task_id":            flags.TaskID,
				"task_name":          detail.GetTask().GetTaskName(),
				"result":             detail.GetTask().GetResult(),
				"delete_result_data": !flags.KeepResult,
			})
		}
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
	flags.BoolVar(&collectorTaskDeleteFlagsValue.KeepResult, "keep-result", false, "只删任务，保留结果数据集")
	flags.BoolVar(&collectorTaskDeleteFlagsValue.Yes, "yes", false, "确认删除；不加只预览")
}

var collectorResultDeleteFlagsValue struct {
	File, SpaceID string
	DatasetIDs    []string
	Yes           bool
}

// collectorResultDeleteCmd 删除已没有任务引用的采集结果（数据集、行数据与它的查询索引）。
var collectorResultDeleteCmd = &cobra.Command{
	Use:   "delete-result",
	Short: "删除没有任务引用的采集结果（数据集、行数据与它的查询索引；默认只预览，--yes 才执行）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		flags := collectorResultDeleteFlagsValue
		if flags.SpaceID == "" || len(flags.DatasetIDs) == 0 {
			return errors.New("必须指定 --space-id 与至少一个 --dataset-id")
		}
		if !flags.Yes {
			return writePreview(cmd, map[string]any{"action": "delete_result", "space_id": flags.SpaceID, "datasets": flags.DatasetIDs})
		}
		metadata, closer, err := newCollectorStorageMetadataClient(cmd.Context(), flags.File)
		if err != nil {
			return err
		}
		defer closer.Close()
		var objects collectorOwnedMetadata
		for _, id := range flags.DatasetIDs {
			objects.datasets = appendUniqueStorageObject(objects.datasets, flags.SpaceID, id)
		}
		if err := deleteCollectorOwnedMetadata(cmd.Context(), metadata, objects); err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), map[string]any{"status": "deleted", "datasets": flags.DatasetIDs})
	},
}

func init() {
	collectorTaskCmd.AddCommand(collectorResultDeleteCmd)
	flags := collectorResultDeleteCmd.Flags()
	flags.StringVar(&collectorResultDeleteFlagsValue.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	flags.StringVar(&collectorResultDeleteFlagsValue.SpaceID, "space-id", "", "空间 ID")
	flags.StringArrayVar(&collectorResultDeleteFlagsValue.DatasetIDs, "dataset-id", nil, "要删除的数据集（连同行数据与查询索引），可重复")
	flags.BoolVar(&collectorResultDeleteFlagsValue.Yes, "yes", false, "确认删除；不加只预览")
}
