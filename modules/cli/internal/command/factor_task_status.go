package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/spf13/cobra"
)

// factorTaskStatus 是一个因子任务在引擎里的状态：能不能计算、逐项检查结果。
type factorTaskStatus struct {
	TaskID      string                      `json:"task_id"`
	Computing   bool                        `json:"computing"`
	Paused      bool                        `json:"paused,omitempty"`
	WarmupState string                      `json:"warmup_state,omitempty"`
	Checks      []*factorpb.FactorTaskCheck `json:"checks,omitempty"`
}

func newFactorTaskStatusCommand() *cobra.Command {
	var file, taskID string
	var asJSON, recheck bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "查看因子任务能不能计算：引擎计算前的逐项检查（窗口载入、数据覆盖、变更同步、回看数量、自定义算子模块）",
		Long: "列出引擎随心跳上报的各因子任务的计算前检查：有不通过项的任务不计算，不通过的项写明原因与处理办法。\n" +
			"引擎每 30 秒重查一次，窗口载入完成后立即重查，检查通过就自动开始计算，不需要任何命令。\n" +
			"引擎模块暂停或恢复追赶时所有任务都不计算（moox-cli lifecycle status 查看）。\n" +
			"加 --recheck（要配合 --task-id）让引擎立即重查这个任务、直接返回结果，例如导入历史数据之后不用等下一轮。\n\n" +
			"对应接口：trpc.moox.factor.FactorMgr GetStatus；--recheck 是 trpc.moox.factor.EngineControl CheckTask。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			gateway, err := openCommandGateway(cmd.Context(), file, nil)
			if err != nil {
				return err
			}
			defer gateway.Close()
			if recheck {
				return runFactorTaskRecheck(cmd.Context(), gateway, strings.TrimSpace(taskID), asJSON, cmd.OutOrStdout())
			}
			return runFactorTaskStatus(cmd.Context(), gateway, strings.TrimSpace(taskID), asJSON, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&recheck, "recheck", false, "让因子引擎立即重查 --task-id 指定的任务并返回结果（不等每 30 秒一轮）")
	cmd.Flags().StringVar(&file, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	cmd.Flags().StringVar(&taskID, "task-id", "", "只看这个因子任务（不给时列出目录里的全部任务）")
	cmd.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	return cmd
}

func runFactorTaskStatus(ctx context.Context, gateway commandGateway, taskID string, asJSON bool, out io.Writer) error {
	rsp := &factorpb.GetStatusRsp{}
	if err := gateway.Invoke(ctx, factorMgr, "GetStatus", &factorpb.GetStatusReq{}, rsp); err != nil {
		return fmt.Errorf("读取因子运行状态：%w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("读取因子运行状态：%s（%s）", rsp.GetRetInfo().GetMsg(), code)
	}
	var tasks []factorTaskStatus
	for _, runner := range rsp.GetRunners() {
		if taskID != "" && runner.GetTaskId() != taskID {
			continue
		}
		task := factorTaskStatus{TaskID: runner.GetTaskId(), Paused: runner.GetPaused(), WarmupState: runner.GetWarmupState(), Checks: runner.GetChecks()}
		task.Computing = !task.Paused && len(task.Checks) > 0 && failedTaskChecks(task.Checks) == 0
		tasks = append(tasks, task)
	}
	if taskID != "" && len(tasks) == 0 {
		return fmt.Errorf("引擎没有上报因子任务 %s：任务不在目录里、没有启用，或引擎不在线", taskID)
	}
	if asJSON {
		return printJSON(out, map[string]any{"engine_online": rsp.GetEngine().GetOnline(), "tasks": tasks})
	}
	if !rsp.GetEngine().GetOnline() {
		fmt.Fprintln(out, "注意：因子引擎不在线，下面是它最后一次上报的状态")
	}
	printFactorTaskStatuses(out, tasks)
	return nil
}

// runFactorTaskRecheck 让引擎立即重查一个任务（EngineControl.CheckTask），打印引擎直接返回的逐项结果。
func runFactorTaskRecheck(ctx context.Context, gateway commandGateway, taskID string, asJSON bool, out io.Writer) error {
	if taskID == "" {
		return errors.New("--recheck 要配合 --task-id 指定任务")
	}
	rsp := &factorpb.CheckTaskRsp{}
	if err := gateway.Invoke(ctx, engineControl, "CheckTask", &factorpb.CheckTaskReq{TaskId: taskID}, rsp); err != nil {
		return fmt.Errorf("让因子引擎重查任务：%w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("让因子引擎重查任务：%s（%s）", rsp.GetRetInfo().GetMsg(), code)
	}
	task := factorTaskStatus{TaskID: taskID, Paused: rsp.GetPaused(), Checks: rsp.GetChecks()}
	task.Computing = !task.Paused && len(task.Checks) > 0 && failedTaskChecks(task.Checks) == 0
	if asJSON {
		return printJSON(out, map[string]any{"engine_online": true, "tasks": []factorTaskStatus{task}})
	}
	printFactorTaskStatuses(out, []factorTaskStatus{task})
	return nil
}

func printFactorTaskStatuses(out io.Writer, tasks []factorTaskStatus) {
	for _, task := range tasks {
		fmt.Fprintf(out, "%s：%s\n", task.TaskID, factorTaskStatusText(task))
		for _, check := range task.Checks {
			if check.GetStatus() == "PASS" {
				continue
			}
			line := fmt.Sprintf("  [%s] %s", check.GetStatus(), check.GetName())
			if check.GetMessage() != "" {
				line += "：" + check.GetMessage()
			}
			fmt.Fprintln(out, line)
			if check.GetRemedy() != "" {
				fmt.Fprintf(out, "      处理：%s\n", check.GetRemedy())
			}
		}
	}
}

func failedTaskChecks(checks []*factorpb.FactorTaskCheck) int {
	n := 0
	for _, check := range checks {
		if check.GetStatus() == "FAIL" {
			n++
		}
	}
	return n
}

func factorTaskStatusText(task factorTaskStatus) string {
	failed := failedTaskChecks(task.Checks)
	switch {
	case len(task.Checks) == 0:
		return "引擎还没有上报检查结果"
	case task.Paused && failed == 0:
		return "检查通过，引擎模块暂停中，暂不计算"
	case task.Paused:
		return fmt.Sprintf("不计算：%d 项检查没通过，引擎模块也在暂停中", failed)
	case failed > 0:
		return fmt.Sprintf("不计算：%d 项检查没通过", failed)
	case task.WarmupState == "warming":
		return "计算中（部分标的的窗口还在积累，结果暂不作为正式数据）"
	}
	return "计算中"
}
