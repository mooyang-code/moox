package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/spf13/cobra"
)

// 函数实例的写操作（创建、部署、删除、改运行配置）要先拿到采集发布租约：同一个空间同一时刻只允许一个发布者，
// 提交时带上租约与围栏令牌，服务端据此拒绝过期的发布者。命令负责获取、续期、释放，用户不用关心；
// 请求体写在 --data 里（与管理台提交的字段一致，租约字段由命令填入）。

type cloudFunctionWrite struct {
	Use         string
	Short       string
	Method      string
	ItemsField  string // 请求里放条目的字段；空表示请求不带条目（删除用顶层 instance_ids）
	Destructive bool
	note        string // 追加到帮助末尾的提醒
}

var cloudFunctionWrites = []cloudFunctionWrite{
	{Use: "create", Short: "创建函数实例（请求体用 --data，字段 instances；创建后用 job 查看进度）", Method: "SubmitCreateFunctionInstances", ItemsField: "instances"},
	{Use: "deploy", Short: "把代码包部署到函数实例（线上切换版本；默认只预览，--yes 才执行）", Method: "SubmitDeployFunctionInstances", ItemsField: "deployments", Destructive: true},
	{Use: "update-config", Short: "修改函数实例的运行配置与定时器（默认只预览，--yes 才执行）", Method: "SubmitUpdateFunctionInstanceRuntimeConfigs", ItemsField: "instances", Destructive: true,
		note: "\n注意：每一项里的 timer_enabled 不写就等于 false（关闭该实例的定时器），要保持开启必须写 true。"},
	{Use: "delete", Short: "删除函数实例（--instance-id 可重复；默认只预览，--yes 才执行）", Method: "SubmitDeleteFunctionInstances", Destructive: true},
}

func init() {
	rpcGroupShort["cloud"] = "云函数：云账户、函数实例、SCF 导入与代码包（调用 CloudFunctionMgr）"
	cloudCmd := &cobra.Command{Use: "cloud", Short: rpcGroupShort["cloud"]}
	functionCmd := &cobra.Command{Use: "function", Short: "函数实例与作业"}
	for _, write := range cloudFunctionWrites {
		functionCmd.AddCommand(write.command())
	}
	cloudCmd.AddCommand(functionCmd)
	rootCmd.AddCommand(cloudCmd)
}

func (w cloudFunctionWrite) command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   w.Use,
		Short: w.Short,
		Long:  w.Short + "\n\n对应接口：" + cloudFunctionMgr + " " + w.Method + "。命令会先获取采集发布租约，提交后一直等作业结束并返回结果，再释放租约（租约要罩住整个异步批次）；中途失联、结果未知时不释放，租约约 2 分钟后自动过期。" + w.note,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, _ := cmd.Flags().GetString("file")
			space, _ := cmd.Flags().GetString("space-id")
			data, _ := cmd.Flags().GetString("data")
			instanceIDs, _ := cmd.Flags().GetStringSlice("instance-id")
			request, err := readRPCData(data, cmd.InOrStdin())
			if err != nil {
				return err
			}
			if w.Method == "SubmitDeleteFunctionInstances" {
				if len(instanceIDs) > 0 {
					request["instance_ids"] = instanceIDs
				}
				if ids, _ := request["instance_ids"].([]any); len(ids) == 0 && len(instanceIDs) == 0 {
					return errors.New("缺少 --instance-id（也可以在 --data 里提供 instance_ids）")
				}
			} else if items, _ := request[w.ItemsField].([]any); len(items) == 0 {
				return fmt.Errorf("缺少请求内容：用 --data 提供 %s", w.ItemsField)
			}
			if w.Destructive {
				if yes, _ := cmd.Flags().GetBool("yes"); !yes {
					return writePreview(cmd, map[string]any{"service": cloudFunctionMgr, "method": w.Method, "space_id": space, "request": request, "note": "执行时命令会自动填入采集发布租约字段"})
				}
			}
			cmd.SilenceUsage = true
			return runCloudFunctionWrite(cmd.Context(), cmd.OutOrStdout(), file, space, w, request)
		},
	}
	cmd.Flags().String("file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	cmd.Flags().String("space-id", "crypto", "空间 ID（发布租约按空间持有）")
	cmd.Flags().String("data", "", "请求 JSON；@文件 读取文件，- 读取标准输入")
	if w.Method == "SubmitDeleteFunctionInstances" {
		cmd.Flags().StringSlice("instance-id", nil, "要删除的实例 ID，可重复或逗号分隔")
	}
	if w.Destructive {
		cmd.Flags().Bool("yes", false, "确认执行；不加只预览将要发出的请求")
	}
	return cmd
}

// injectPublishFence 把租约与围栏令牌写进请求：条目里每一项各带一份，删除请求写在顶层。
func injectPublishFence(request map[string]any, w cloudFunctionWrite, fence *adminclient.CollectorPublishFence) {
	if w.ItemsField == "" {
		request["collector_publish_lease_id"] = fence.LeaseID
		request["collector_publish_fencing_token"] = fence.FencingToken
		return
	}
	items, _ := request[w.ItemsField].([]any)
	for _, item := range items {
		if entry, ok := item.(map[string]any); ok {
			entry["collector_publish_lease_id"] = fence.LeaseID
			entry["collector_publish_fencing_token"] = fence.FencingToken
		}
	}
}

// runCloudFunctionWrite 持有发布租约提交并等到作业结束：
//   - 服务端明确拒绝（没受理）：立即释放租约；
//   - 受理后作业到终态：释放；
//   - 传输中断或等待超时（结果未知，作业可能还在跑）：不释放，让租约自然过期，避免别的发布者在批次进行中插进来。
func runCloudFunctionWrite(ctx context.Context, out io.Writer, file, space string, w cloudFunctionWrite, request map[string]any) error {
	gateway, err := openCommandGateway(ctx, file, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	client := &adminclient.Client{Gateway: gateway, SpaceID: space}
	leaseCtx, guard, err := acquireCollectorPublishLease(ctx, client, space)
	if err != nil {
		return fmt.Errorf("获取采集发布租约失败：%w", err)
	}
	fence := client.CollectorPublishFence()
	if fence == nil {
		guard.Abandon()
		return errors.New("没有拿到发布租约")
	}
	injectPublishFence(request, w, fence)
	var response json.RawMessage
	if err := client.CallGatewayJSON(leaseCtx, cloudFunctionMgr, w.Method, request, &response); err != nil {
		guard.Abandon()
		return err
	}
	if err := rpcRetError(response); err != nil {
		_ = printRPCResponse(out, response, true)
		_ = guard.Close()
		return err
	}
	var submitted struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(response, &submitted)
	if submitted.JobID == "" {
		_ = guard.Close()
		return printRPCResponse(out, response, true)
	}
	waitCtx, cancel := context.WithTimeout(leaseCtx, 30*time.Minute)
	defer cancel()
	change, err := waitCollectorBatchTerminalChange(waitCtx, client, submitted.JobID)
	if err != nil {
		guard.Abandon()
		return err
	}
	if closeErr := guard.Close(); closeErr != nil {
		fmt.Fprintf(out, "提示：释放发布租约失败：%v\n", closeErr)
	}
	if err := printJSON(out, change); err != nil {
		return err
	}
	if change != nil && change.Job != nil && !strings.Contains(strings.ToUpper(change.Job.Status), "SUCCESS") {
		return fmt.Errorf("作业 %s 没有全部成功：%s", submitted.JobID, change.Job.Status)
	}
	return nil
}
