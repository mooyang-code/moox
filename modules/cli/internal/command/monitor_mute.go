package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/spf13/cobra"
)

// 告警屏蔽：给初始数据导入等会让数据链路告警暂时失真的操作用。屏蔽期间 Monitor 的探测、入库、告警评估与记录照常，
// 只是不推送到通知渠道；到期自动恢复，结束时补发一条汇总（仍未恢复的告警，以及期间触发后又恢复的）。

// maxMuteDuration 与 Monitor 的上限一致（24 小时），在命令行先拦一次，给出更直接的提示。
const maxMuteDuration = 24 * time.Hour

var monitorFlags struct {
	File   string
	For    time.Duration
	Reason string
}

var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Monitor 运维：告警屏蔽",
}

var monitorMuteCmd = &cobra.Command{
	Use:   "mute",
	Short: "屏蔽告警推送一段时间（到期自动恢复，最长 24 小时）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if monitorFlags.For <= 0 || monitorFlags.For > maxMuteDuration {
			return errors.New("--for 必须大于 0 且不超过 24h，例如 --for 3h")
		}
		return runMonitorMute(cmd.Context(), cmd.OutOrStdout(), monitorFlags.File, uint32(monitorFlags.For/time.Second), monitorFlags.Reason)
	},
}

var monitorUnmuteCmd = &cobra.Command{
	Use:   "unmute",
	Short: "提前结束告警屏蔽，并补发一条汇总",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runMonitorMuteCall(cmd.Context(), cmd.OutOrStdout(), monitorFlags.File, "UnmuteAlerts")
	},
}

var monitorMuteStatusCmd = &cobra.Command{
	Use:   "mute-status",
	Short: "查看告警屏蔽状态",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runMonitorMuteCall(cmd.Context(), cmd.OutOrStdout(), monitorFlags.File, "GetAlertMute")
	},
}

func init() {
	rootCmd.AddCommand(monitorCmd)
	monitorCmd.AddCommand(monitorMuteCmd, monitorUnmuteCmd, monitorMuteStatusCmd)
	monitorCmd.PersistentFlags().StringVar(&monitorFlags.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	monitorMuteCmd.Flags().DurationVar(&monitorFlags.For, "for", 0, "屏蔽多久，例如 3h（必填，最长 24h）")
	monitorMuteCmd.Flags().StringVar(&monitorFlags.Reason, "reason", "", "原因，写进屏蔽详情")
}

func runMonitorMute(ctx context.Context, out io.Writer, file string, seconds uint32, reason string) error {
	gateway, err := openCommandGateway(ctx, file, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &monitorpb.MuteAlertsRsp{}
	if err := invokeMonitor(ctx, gateway, "MuteAlerts", &monitorpb.MuteAlertsReq{DurationSeconds: seconds, Reason: reason}, rsp, rsp.GetRetInfo); err != nil {
		return err
	}
	return printMuteState(out, rsp.GetState())
}

func runMonitorMuteCall(ctx context.Context, out io.Writer, file, method string) error {
	gateway, err := openCommandGateway(ctx, file, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	switch method {
	case "UnmuteAlerts":
		rsp := &monitorpb.UnmuteAlertsRsp{}
		if err := invokeMonitor(ctx, gateway, method, &monitorpb.UnmuteAlertsReq{}, rsp, rsp.GetRetInfo); err != nil {
			return err
		}
		return printMuteState(out, rsp.GetState())
	case "GetAlertMute":
		rsp := &monitorpb.GetAlertMuteRsp{}
		if err := invokeMonitor(ctx, gateway, method, &monitorpb.GetAlertMuteReq{}, rsp, rsp.GetRetInfo); err != nil {
			return err
		}
		return printMuteState(out, rsp.GetState())
	}
	return fmt.Errorf("unsupported monitor method %q", method)
}

func printMuteState(out io.Writer, state *monitorpb.AlertMuteState) error {
	return printJSON(out, map[string]any{
		"muted": state.GetMuted(), "since": state.GetSince(), "expires_at": state.GetExpiresAt(),
		"reason": state.GetReason(), "by": state.GetBy(),
	})
}
