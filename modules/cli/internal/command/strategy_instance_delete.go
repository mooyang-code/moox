package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/spf13/cobra"
	"trpc.group/trpc-go/trpc-go/errs"
)

// strategyInstanceDeleteIncomplete 是删除超时或中途失败时的提示：删除每一步都幂等，重试安全。
const strategyInstanceDeleteIncomplete = "删除未完成，请稍后重试"

// strategyDeleteTask 是预览里的一个因子任务：随实例删除的任务与它的结果数据集。
type strategyDeleteTask struct {
	Market          string `json:"market"`
	TaskID          string `json:"task_id"`
	Name            string `json:"name,omitempty"`
	ResultDatasetID string `json:"result_dataset_id,omitempty"`
	// State：to_delete 将删除；already_deleted 上次删除时已经删掉了。
	State string `json:"state"`
}

// strategyDeleteInstance 是 GetStrategyInstance 里删除要用到的字段（CLI 不依赖策略模块的协议包，按 JSON 取）。
type strategyDeleteInstance struct {
	InstanceID         string            `json:"instance_id"`
	StrategyID         string            `json:"strategy_id"`
	Enabled            bool              `json:"enabled"`
	SessionID          string            `json:"session_id"`
	LogicalAccountID   string            `json:"logical_account_id"`
	FactorTasks        map[string]string `json:"factor_tasks"`
	MissingFactorTasks []string          `json:"missing_factor_tasks"`
}

func init() {
	registerRPCExtra([]string{"strategy", "instance"}, newStrategyInstanceDeleteCommand)
}

func newStrategyInstanceDeleteCommand() *cobra.Command {
	var file, spaceID, instanceID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "删除已停用的策略实例，连同它关联的因子任务和结果数据集（默认只预览，--yes 才执行）",
		Long: "删除已停用、没有运行会话的策略实例，连同它关联的因子任务和任务的结果数据集，结果数据不可恢复。\n" +
			"结果数据集有归档任务时拒绝，先在归档页删掉归档任务。不带 --yes 只列出将要删除的因子任务和数据集；\n" +
			"中途失败或超时后再执行一次即可接着删完。Trade 里的历史目标与订单不删。\n\n" +
			"对应接口：trpc.moox.strategy.StrategyMgr DeleteStrategyInstance（破坏性，默认只预览）。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(instanceID) == "" {
				return errors.New("缺少 --id")
			}
			cmd.SilenceUsage = true
			gateway, err := openCommandGateway(cmd.Context(), file, nil)
			if err != nil {
				return err
			}
			defer gateway.Close()
			return runStrategyInstanceDelete(cmd.Context(), cmd, gateway, spaceID, strings.TrimSpace(instanceID), yes)
		},
	}
	cmd.Flags().StringVar(&file, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	cmd.Flags().StringVar(&spaceID, "space-id", "crypto", "空间 ID")
	cmd.Flags().StringVar(&instanceID, "id", "", "实例 ID（必填）")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认执行；不加只列出将要删除的内容")
	return cmd
}

func runStrategyInstanceDelete(ctx context.Context, cmd *cobra.Command, gateway commandGateway, spaceID, instanceID string, yes bool) error {
	client := &adminclient.Client{Gateway: gateway, SpaceID: spaceID}
	instance, found, err := readStrategyDeleteInstance(ctx, client, instanceID)
	if err != nil {
		return err
	}
	if !found {
		return printJSON(cmd.OutOrStdout(), map[string]any{"status": "not_found", "instance_id": instanceID, "message": "实例不存在，可能已经删除"})
	}
	tasks, err := strategyDeleteTasks(ctx, gateway, spaceID, instance)
	if err != nil {
		return err
	}
	if !yes {
		preview := map[string]any{
			"action": "delete_strategy_instance", "space_id": spaceID, "instance_id": instance.InstanceID, "strategy_id": instance.StrategyID,
			"factor_tasks": tasks, "warning": "因子任务和结果数据集会一起删除，结果数据不可恢复；Trade 里的历史目标与订单不删",
		}
		if instance.LogicalAccountID != "" {
			preview["logical_account_id"] = instance.LogicalAccountID
		}
		if blocker := strategyDeleteBlocker(instance); blocker != "" {
			preview["blocked"] = blocker
		}
		return writePreview(cmd, preview)
	}
	var response json.RawMessage
	if err := client.CallGatewayJSON(ctx, strategyMgr, "DeleteStrategyInstance", map[string]any{"instance_id": instanceID}, &response); err != nil {
		if strategyDeleteTimedOut(err) {
			return fmt.Errorf("%s：%w", strategyInstanceDeleteIncomplete, err)
		}
		return err
	}
	if err := rpcRetError(response); err != nil {
		var business *rpcBusinessError
		if errors.As(err, &business) && strings.Contains(business.Msg, "超时") {
			return fmt.Errorf("%s：%s", strategyInstanceDeleteIncomplete, business.Msg)
		}
		return err
	}
	return printJSON(cmd.OutOrStdout(), map[string]any{"status": "deleted", "instance_id": instanceID, "factor_tasks": tasks})
}

// readStrategyDeleteInstance 读取实例；实例不存在时 found 为假。
func readStrategyDeleteInstance(ctx context.Context, client *adminclient.Client, instanceID string) (strategyDeleteInstance, bool, error) {
	var rsp struct {
		RetInfo struct {
			Code json.RawMessage `json:"code"`
			Msg  string          `json:"msg"`
		} `json:"ret_info"`
		Instance strategyDeleteInstance `json:"instance"`
	}
	if err := client.CallGatewayJSON(ctx, strategyMgr, "GetStrategyInstance", map[string]any{"instance_id": instanceID}, &rsp); err != nil {
		return strategyDeleteInstance{}, false, fmt.Errorf("读取策略实例：%w", err)
	}
	switch code := strings.Trim(string(rsp.RetInfo.Code), `"`); code {
	case "", "0", "SUCCESS":
		return rsp.Instance, true, nil
	case "5", "NOT_FOUND":
		return strategyDeleteInstance{}, false, nil
	default:
		return strategyDeleteInstance{}, false, fmt.Errorf("读取策略实例：%s (code %s)", rsp.RetInfo.Msg, code)
	}
}

// strategyDeleteTasks 按市场列出实例的因子任务与结果数据集；Factor 里已经不存在的任务标为已删除。
func strategyDeleteTasks(ctx context.Context, gateway commandGateway, spaceID string, instance strategyDeleteInstance) ([]strategyDeleteTask, error) {
	var tasks []strategyDeleteTask
	for _, market := range []string{"spot", "swap", "equity", "multi"} {
		taskID, ok := instance.FactorTasks[market]
		if !ok {
			continue
		}
		rsp := &factorpb.GetFactorTaskRsp{}
		if err := gateway.Invoke(ctx, factorMgr, "GetFactorTask", &factorpb.GetFactorTaskReq{TaskId: taskID}, rsp); err != nil {
			return nil, fmt.Errorf("读取因子任务 %s：%w", taskID, err)
		}
		task := strategyDeleteTask{Market: market, TaskID: taskID, State: "to_delete"}
		switch rsp.GetRetInfo().GetCode() {
		case commonpb.ErrorCode_SUCCESS:
			task.Name, task.ResultDatasetID = rsp.GetFactorTask().GetName(), rsp.GetFactorTask().GetResultDatasetId()
		case commonpb.ErrorCode_NOT_FOUND:
			task.State = "already_deleted"
		default:
			return nil, fmt.Errorf("读取因子任务 %s：%s (code %d)", taskID, rsp.GetRetInfo().GetMsg(), rsp.GetRetInfo().GetCode())
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// strategyDeleteBlocker 是预览里提前说明的拒绝原因（执行时服务端同样会拒绝）。
func strategyDeleteBlocker(instance strategyDeleteInstance) string {
	switch {
	case instance.Enabled:
		return "实例启用中，先用 strategy instance disable 停用"
	case instance.SessionID != "":
		return "实例的启用或停用操作尚未完成，稍后再删除"
	}
	return ""
}

// strategyDeleteTimedOut 判断调用结果是否未知（超时、连接中断）：删除可能已经做了一部分。
func strategyDeleteTimedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	switch errs.Code(err) {
	case errs.RetClientTimeout, errs.RetClientFullLinkTimeout, errs.RetServerTimeout, errs.RetServerFullLinkTimeout, errs.RetClientNetErr, errs.RetClientReadFrameErr:
		return true
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout")
}
