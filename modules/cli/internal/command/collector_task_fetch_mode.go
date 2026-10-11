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

type collectorTaskFetchModeFlags struct {
	File       string
	SpaceID    string
	TaskIDs    []string
	Mode       string
	Until      string
	From       string
	ClearUntil bool
	ClearFrom  bool
	Symbols    int
	Lateness   int
}

var collectorTaskFetchModeFlagsValue collectorTaskFetchModeFlags

var collectorTaskFetchModeCmd = &cobra.Command{
	Use:   "fetch-mode",
	Short: "设置采集任务的实时采集归属（invoke 或 timer）与交接边界",
	Long: "fetch_mode、invoke_until_period、invoke_from_period 在同一次更新里写入，避免出现没有边界的旧调度。\n" +
		"切换到 SCF 定时触发：先给任务设置 --until T（整点，旧链路只负责目标周期 < T），\n" +
		"T 之后旧链路处理完在途批次，再执行 --mode timer --clear-until。\n" +
		"回退：选新的整点 T'，先设置 --from T'（旧链路只负责 ≥ T'），确认新函数完成 < T' 的最后一轮后执行 --mode invoke；\n" +
		"稳定后 --clear-from。时间用 RFC3339，必须是整点。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runCollectorTaskFetchMode(cmd.Context(), cmd.OutOrStdout(), collectorTaskFetchModeFlagsValue)
	},
}

func init() {
	collectorTaskCmd.AddCommand(collectorTaskFetchModeCmd)
	flags := collectorTaskFetchModeCmd.Flags()
	flags.StringVar(&collectorTaskFetchModeFlagsValue.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	flags.StringVar(&collectorTaskFetchModeFlagsValue.SpaceID, "space-id", "", "空间 ID")
	flags.StringSliceVar(&collectorTaskFetchModeFlagsValue.TaskIDs, "task-id", nil, "任务 ID，可重复指定")
	flags.StringVar(&collectorTaskFetchModeFlagsValue.Mode, "mode", "", "实时采集归属：invoke 或 timer；留空保持不变")
	flags.StringVar(&collectorTaskFetchModeFlagsValue.Until, "until", "", "invoke_until_period：旧链路只负责目标周期早于该时间的批次")
	flags.StringVar(&collectorTaskFetchModeFlagsValue.From, "from", "", "invoke_from_period：旧链路只负责目标周期不早于该时间的批次")
	flags.BoolVar(&collectorTaskFetchModeFlagsValue.ClearUntil, "clear-until", false, "清除 invoke_until_period")
	flags.BoolVar(&collectorTaskFetchModeFlagsValue.ClearFrom, "clear-from", false, "清除 invoke_from_period")
	flags.IntVar(&collectorTaskFetchModeFlagsValue.Symbols, "symbols-per-function", 0, "每个函数负责的标的数（0 保持不变）")
	flags.IntVar(&collectorTaskFetchModeFlagsValue.Lateness, "allowed-lateness-seconds", 0, "结果数据集的允许迟到时间（0 保持不变）")
}

func runCollectorTaskFetchMode(ctx context.Context, out io.Writer, flags collectorTaskFetchModeFlags) error {
	if strings.TrimSpace(flags.SpaceID) == "" || len(flags.TaskIDs) == 0 {
		return errors.New("--space-id and at least one --task-id are required")
	}
	if flags.Mode != "" && flags.Mode != "invoke" && flags.Mode != "timer" {
		return errors.New("--mode must be invoke or timer")
	}
	if (flags.Until != "" && flags.ClearUntil) || (flags.From != "" && flags.ClearFrom) {
		return errors.New("a boundary cannot be set and cleared at the same time")
	}
	until, from := "", ""
	var err error
	if flags.Until != "" {
		if until, err = parseKlineTime("until", flags.Until); err != nil {
			return err
		}
	}
	if flags.From != "" {
		if from, err = parseKlineTime("from", flags.From); err != nil {
			return err
		}
	}
	for _, boundary := range []string{until, from} {
		if boundary != "" && !strings.HasSuffix(boundary, ":00:00Z") {
			return fmt.Errorf("boundary %s must be on the hour", boundary)
		}
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	results := make([]map[string]any, 0, len(flags.TaskIDs))
	for _, taskID := range flags.TaskIDs {
		detail := &collectorpb.GetTaskDetailRsp{}
		if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "GetTaskDetail", &collectorpb.GetTaskDetailReq{SpaceId: flags.SpaceID, TaskId: taskID}, detail); err != nil {
			return fmt.Errorf("get task %s: %w", taskID, err)
		}
		if err := collectorRetError("get task "+taskID, detail.GetRetInfo()); err != nil {
			return err
		}
		task := detail.GetTask()
		params := task.GetCollectParams().AsMap()
		if flags.Mode != "" {
			params["fetch_mode"] = flags.Mode
		}
		apply := func(key, value string, clear bool) {
			if clear {
				delete(params, key)
			} else if value != "" {
				params[key] = value
			}
		}
		apply("invoke_until_period", until, flags.ClearUntil)
		apply("invoke_from_period", from, flags.ClearFrom)
		if flags.Symbols > 0 {
			params["symbols_per_function"] = flags.Symbols
		}
		if flags.Lateness > 0 {
			params["allowed_lateness_seconds"] = flags.Lateness
		}
		structured, err := structpb.NewStruct(params)
		if err != nil {
			return fmt.Errorf("task %s params: %w", taskID, err)
		}
		task.CollectParams = structured
		rsp := &collectorpb.UpdateTaskRsp{}
		if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "UpdateTask", &collectorpb.UpdateTaskReq{SpaceId: flags.SpaceID, TaskId: taskID, Task: task}, rsp); err != nil {
			return fmt.Errorf("update task %s: %w", taskID, err)
		}
		if err := collectorRetError("update task "+taskID, rsp.GetRetInfo()); err != nil {
			return err
		}
		results = append(results, map[string]any{"task_id": taskID, "fetch_mode": params["fetch_mode"], "invoke_until_period": params["invoke_until_period"], "invoke_from_period": params["invoke_from_period"]})
	}
	return printJSON(out, map[string]any{"status": "updated", "tasks": results})
}
