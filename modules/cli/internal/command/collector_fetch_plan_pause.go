package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/spf13/cobra"
)

type fetchPlanPauseConfig struct {
	File    string
	SpaceID string
	Wait    time.Duration
}

var (
	fetchPlanPauseFlags fetchPlanPauseConfig
	fetchPlanPauseYes   bool
)

var collectorFetchPlanPauseCmd = &cobra.Command{
	Use:   "pause",
	Short: "暂停空间的采集：停用全部 SCF 定时器，并停止采集计划对账与补采（默认只预览，--yes 才执行）",
	Long: "保存暂停开关（重启后保持），并等 Collector 在下一轮同步（约 1 分钟内）停用该空间全部函数的定时器，云端读回确认后返回。\n" +
		"定时器停用只保证不再触发新的采集调用，已经触发、仍在执行的调用不在其内：返回后还要再等过 SCF 函数的执行超时，\n" +
		"并确认 Storage 不再有该空间的写入，才能认为采集已经静默。暂停期间产生的周期不会进入账本，也没有完成事件，\n" +
		"恢复后行由自动补采补齐，因子结果需要用因子管理 CLI 的 recalc 补算。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if !fetchPlanPauseYes {
			return writePreview(cmd, map[string]any{"action": "pause_fetch_plan", "space_id": fetchPlanPauseFlags.SpaceID})
		}
		return runFetchPlanPause(cmd.Context(), cmd.OutOrStdout(), fetchPlanPauseFlags, true, fetchPlanPauseFlags.Wait)
	},
}

func init() {
	collectorFetchPlanPauseCmd.Flags().StringVar(&fetchPlanPauseFlags.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	collectorFetchPlanPauseCmd.Flags().StringVar(&fetchPlanPauseFlags.SpaceID, "space-id", "", "空间 ID")
	collectorFetchPlanPauseCmd.Flags().BoolVar(&fetchPlanPauseYes, "yes", false, "确认暂停；不加只预览")
	collectorFetchPlanPauseCmd.Flags().DurationVar(&fetchPlanPauseFlags.Wait, "wait", 5*time.Minute, "等待云端确认定时器全部停用的最长时间；0 表示只保存开关不等待")
}

func collectorRetError(action string, ret *collectorpb.RetInfo) error {
	if ret.GetCode() != collectorpb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s (code %d)", action, ret.GetMsg(), ret.GetCode())
	}
	return nil
}

// runFetchPlanPause 保存暂停（或恢复）开关；暂停时轮询到云端确认定时器全部停用或超时。
func runFetchPlanPause(ctx context.Context, out io.Writer, flags fetchPlanPauseConfig, paused bool, wait time.Duration) error {
	if strings.TrimSpace(flags.SpaceID) == "" {
		return errors.New("--space-id 必填")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	call := func() (*collectorpb.SetFetchPlanPausedRsp, error) {
		rsp := &collectorpb.SetFetchPlanPausedRsp{}
		if err := gateway.Invoke(ctx, collectMgr, "SetFetchPlanPaused", &collectorpb.SetFetchPlanPausedReq{SpaceId: flags.SpaceID, Paused: paused}, rsp); err != nil {
			return nil, fmt.Errorf("set fetch plan paused: %w", err)
		}
		return rsp, collectorRetError("set fetch plan paused", rsp.GetRetInfo())
	}
	rsp, err := call()
	if err != nil {
		return err
	}
	if !paused {
		return printJSON(out, map[string]any{"status": "resumed", "space_id": flags.SpaceID})
	}
	deadline := time.Now().Add(wait)
	for wait > 0 && !rsp.GetTimersDisabled() {
		if time.Now().After(deadline) {
			return fmt.Errorf("已保存暂停开关，但 %s 内没有确认定时器全部停用；稍后重新执行 pause 查看状态", wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
		if rsp, err = call(); err != nil {
			return err
		}
	}
	return printJSON(out, map[string]any{
		"status": "paused", "space_id": flags.SpaceID, "timers_disabled": rsp.GetTimersDisabled(),
		"note":        "已触发、仍在执行的采集调用不在其内：再等过 SCF 函数的执行超时，并确认 Storage 不再有该空间的写入",
		"factor_note": "该空间上若有启用的因子任务，下一次系统暂停恢复会卡在因子追赶：恢复前先停用这些任务（moox-cli factor task disable --task-id <因子任务 ID> --yes），放行后再启用",
	})
}
