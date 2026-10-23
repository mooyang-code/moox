package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/spf13/cobra"
)

// moox-cli lifecycle reset-*：单个模块的重置（设计第 5 节）。重置是有损、不可逆的：先暂停，再 reset-plan 看计划拿令牌，
// 再 reset-apply 凭令牌发起；失败或中断后凭 operation_id 续做，或 reset-abort 放弃。
// 重置要用**重置专用密钥**签名（MOOX_HEALTH_RESET_ACCESS_KEY / MOOX_HEALTH_RESET_SECRET_KEY），
// 运行时的健康密钥没有 reset 权限；模块进程也要配置同一对重置密钥才开放重置接口。

type resetOptions struct {
	Scope       string
	Token       string
	OperationID string
	Yes         bool
	Wait        bool
	Timeout     time.Duration
}

var resetFlags = resetOptions{Wait: true, Timeout: 30 * time.Minute}

func init() {
	plan := &cobra.Command{
		Use: "reset-plan", Short: "重置计划：只读，列出每一步、保留什么、不备份什么，并给出一次性令牌（10 分钟）", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runResetPlan(cmd.Context(), lifecycleFlags, resetFlags, cmd.OutOrStdout())
		},
	}
	plan.Flags().StringVar(&resetFlags.Scope, "scope", "all", "重置范围：data（只清运行数据，保留配置）| all（再清业务配置，回到新部署的状态）")
	apply := &cobra.Command{
		Use: "reset-apply", Short: "发起重置（--token --scope）或续做（--operation-id）；模块必须已暂停", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runResetApply(cmd.Context(), lifecycleFlags, resetFlags, cmd.OutOrStdout())
		},
	}
	apply.Flags().StringVar(&resetFlags.Scope, "scope", "", "重置范围，要和 plan 一致")
	apply.Flags().StringVar(&resetFlags.Token, "token", "", "reset-plan 给出的令牌（10 分钟有效，只能用一次）")
	apply.Flags().StringVar(&resetFlags.OperationID, "operation-id", "", "续做失败或中断的重置（不需要令牌）")
	apply.Flags().BoolVar(&resetFlags.Yes, "yes", false, "确认执行（会删除数据，不可撤销；执行前模块会先备份）")
	apply.Flags().BoolVar(&resetFlags.Wait, "wait", true, "等重置结束并打印每一步")
	apply.Flags().DurationVar(&resetFlags.Timeout, "timeout", 30*time.Minute, "等待重置结束的时限")
	status := &cobra.Command{
		Use: "reset-status", Short: "查看重置状态与步骤日志", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runResetStatus(cmd.Context(), lifecycleFlags, cmd.OutOrStdout())
		},
	}
	abort := &cobra.Command{
		Use: "reset-abort", Short: "放弃失败的重置：模块置为未初始化，已做的步骤不会回来", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runResetAbort(cmd.Context(), lifecycleFlags, resetFlags, cmd.OutOrStdout())
		},
	}
	abort.Flags().BoolVar(&resetFlags.Yes, "yes", false, "确认放弃")
	lifecycleCmd.AddCommand(plan, apply, status, abort)
}

// resetClient 用重置专用密钥签名；没有配置时报错，不回退到运行时密钥（那个密钥没有 reset 权限）。
func (o lifecycleOptions) resetClient() (*lifecycle.Client, error) {
	access, secret := os.Getenv("MOOX_HEALTH_RESET_ACCESS_KEY"), os.Getenv("MOOX_HEALTH_RESET_SECRET_KEY")
	if access == "" || secret == "" {
		return nil, errors.New("缺少重置专用密钥：设置 MOOX_HEALTH_RESET_ACCESS_KEY / MOOX_HEALTH_RESET_SECRET_KEY（运行时的健康密钥没有 reset 权限）")
	}
	return &lifecycle.Client{Endpoint: o.Endpoint, AuthVersion: os.Getenv("MOOX_HEALTH_AUTH_VERSION"), AccessKey: access, SecretKey: secret}, nil
}

func runResetPlan(ctx context.Context, o lifecycleOptions, r resetOptions, out io.Writer) error {
	client, err := o.resetClient()
	if err != nil {
		return err
	}
	plan, err := client.ResetPlan(ctx, lifecycle.ResetScope(r.Scope))
	if err != nil {
		return err
	}
	if o.JSON {
		return writeLifecycleJSON(out, plan)
	}
	fmt.Fprintf(out, "模块 %s 的重置计划（范围 %s）\n", plan.Module, plan.Scope)
	for i, s := range plan.Steps {
		fmt.Fprintf(out, "  %d. %s：%s\n", i+1, s.Name, s.Describe)
	}
	if len(plan.Keeps) > 0 {
		fmt.Fprintf(out, "保留：%s\n", strings.Join(plan.Keeps, "；"))
	}
	for _, n := range plan.Notes {
		fmt.Fprintf(out, "说明：%s\n", n)
	}
	if len(plan.Stats) > 0 {
		fmt.Fprintf(out, "统计：%v\n", plan.Stats)
	}
	if len(plan.Blockers) > 0 {
		fmt.Fprintln(out, "不能执行：")
		for _, b := range plan.Blockers {
			fmt.Fprintf(out, "  - %s\n", b)
		}
		return errors.New("存在阻止重置的条件，先处理")
	}
	fmt.Fprintf(out, "令牌（%s 前有效，只能用一次）：%s\n", plan.ExpiresAt.Local().Format("15:04:05"), plan.Token)
	fmt.Fprintf(out, "执行：moox-cli lifecycle reset-apply --endpoint %s --scope %s --token %s --yes\n", o.Endpoint, plan.Scope, plan.Token)
	return nil
}

func runResetApply(ctx context.Context, o lifecycleOptions, r resetOptions, out io.Writer) error {
	if !r.Yes {
		return errors.New("重置会删除数据且不可撤销，需要加 --yes 确认（先 reset-plan 看计划）")
	}
	if r.OperationID == "" && (r.Token == "" || r.Scope == "") {
		return errors.New("发起重置需要 --token 和 --scope（先 reset-plan）；续做用 --operation-id")
	}
	client, err := o.resetClient()
	if err != nil {
		return err
	}
	by := os.Getenv("USER")
	if host, err := os.Hostname(); err == nil {
		by = "cli:" + by + "@" + host
	}
	st, err := client.ResetApply(ctx, lifecycle.ResetApplyRequest{Token: r.Token, Scope: lifecycle.ResetScope(r.Scope), OperationID: r.OperationID, By: by})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "重置已开始：%s\n", st.Operation.OperationID)
	if !r.Wait {
		return nil
	}
	return waitReset(ctx, client, r.Timeout, o.JSON, out)
}

// waitReset 轮询到重置结束（完成、失败），逐步打印进度。
func waitReset(ctx context.Context, client *lifecycle.Client, timeout time.Duration, asJSON bool, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	printed := map[string]string{}
	for {
		st, err := client.ResetStatus(ctx)
		if err != nil {
			return err
		}
		if st.Operation != nil {
			for _, s := range st.Operation.Steps {
				if printed[s.Name] != s.State && s.State != "pending" {
					printed[s.Name] = s.State
					fmt.Fprintf(out, "  [%s] %s：%s\n", resetStateText(s.State), s.Name, s.Describe)
					if s.Error != "" {
						fmt.Fprintf(out, "      错误：%s\n", s.Error)
					}
				}
			}
			switch st.Operation.State {
			case "done":
				if asJSON {
					return writeLifecycleJSON(out, st)
				}
				fmt.Fprintf(out, "重置完成：模块回到未初始化，用 moox-cli lifecycle init 初始化后才会工作（备份在模块数据目录的 reset-backups 下）\n")
				return nil
			case "failed":
				fmt.Fprintf(out, "重置失败：%s\n续做：moox-cli lifecycle reset-apply --operation-id %s --yes；放弃：reset-abort\n", st.Operation.Error, st.Operation.OperationID)
				return errors.New("重置失败")
			case "aborted":
				return errors.New("重置已被放弃")
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待重置结束超时（重置仍在后台进行，用 reset-status 查看）：%w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

func resetStateText(s string) string {
	switch s {
	case "done":
		return "完成"
	case "failed":
		return "失败"
	case "running":
		return "进行中"
	case "pending":
		return "待执行"
	case "awaiting":
		return "等待人工"
	}
	return s
}

func runResetStatus(ctx context.Context, o lifecycleOptions, out io.Writer) error {
	st, err := o.client().ResetStatus(ctx)
	if err != nil {
		return err
	}
	if o.JSON {
		return writeLifecycleJSON(out, st)
	}
	fmt.Fprintf(out, "模块 %s：%s\n", st.Module, stageText(st.Stage))
	if st.LastReset != nil {
		fmt.Fprintf(out, "最近一次完成的重置：%s（范围 %s，%s）\n", st.LastReset.OperationID, st.LastReset.Scope, st.LastReset.At.Local().Format("2006-01-02 15:04:05"))
	}
	if st.Operation == nil {
		fmt.Fprintln(out, "没有重置操作记录")
		return nil
	}
	fmt.Fprintf(out, "操作 %s：%s（范围 %s）\n", st.Operation.OperationID, st.Operation.State, st.Operation.Scope)
	for _, s := range st.Operation.Steps {
		fmt.Fprintf(out, "  [%s] %s\n", resetStateText(s.State), s.Name)
	}
	if st.Operation.Error != "" {
		fmt.Fprintf(out, "错误：%s\n", st.Operation.Error)
	}
	return nil
}

func runResetAbort(ctx context.Context, o lifecycleOptions, r resetOptions, out io.Writer) error {
	if !r.Yes {
		return errors.New("放弃重置后已做的步骤不会回来，需要加 --yes 确认")
	}
	client, err := o.resetClient()
	if err != nil {
		return err
	}
	st, err := client.ResetAbort(ctx, os.Getenv("USER"))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "已放弃重置 %s；模块处于%s，保持「重置后等待初始化」\n", st.Operation.OperationID, stageText(st.Stage))
	return nil
}
