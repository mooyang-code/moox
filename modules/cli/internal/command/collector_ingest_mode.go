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

type ingestModeFlags struct {
	File      string
	SpaceID   string
	DatasetID string
	Mode      string
	Executor  string
	Force     bool
	Yes       bool
	Wait      time.Duration
}

var (
	ingestModeGetFlags ingestModeFlags
	ingestModeSetFlags ingestModeFlags
)

var collectorIngestModeCmd = &cobra.Command{Use: "ingest-mode", Short: "按数据集查看与切换采集方式（stream WS 推送 / pull 定时拉取）"}

var collectorIngestModeGetCmd = &cobra.Command{
	Use:   "get",
	Short: "查看数据集的采集方式、执行端、交接中的生效周期与 collector-stream 实例状态",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runIngestModeGet(cmd.Context(), cmd.OutOrStdout(), ingestModeGetFlags)
	},
}

var collectorIngestModeSetCmd = &cobra.Command{
	Use:   "set",
	Short: "切换数据集的采集方式（按生效周期交接；不带 --dataset-id 表示空间全部数据集）",
	Long: "切到 stream 要求 collector-stream 在线；--executor 选拉取与补采的执行端（scf 云函数或 local 本地执行端；留空时 stream 默认 local，pull 默认 scf）。\n" +
		"--force 用于旧执行端失联的极端故障：不等旧执行端确认，已打开的计划也不再等失联端。\n" +
		"不带 --yes 时只打印当前状态；带 --yes 时保存并等待对账生成交接计划，打印每个数据集按频率选用的生效周期 P。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runIngestModeSet(cmd.Context(), cmd.OutOrStdout(), ingestModeSetFlags)
	},
}

func init() {
	collectorCmd.AddCommand(collectorIngestModeCmd)
	collectorIngestModeCmd.AddCommand(collectorIngestModeGetCmd, collectorIngestModeSetCmd)
	for _, item := range []struct {
		cmd   *cobra.Command
		flags *ingestModeFlags
	}{{collectorIngestModeGetCmd, &ingestModeGetFlags}, {collectorIngestModeSetCmd, &ingestModeSetFlags}} {
		item.cmd.Flags().StringVar(&item.flags.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
		item.cmd.Flags().StringVar(&item.flags.SpaceID, "space-id", "", "空间 ID")
		item.cmd.Flags().StringVar(&item.flags.DatasetID, "dataset-id", "", "只处理指定数据集；留空表示空间全部数据集")
	}
	collectorIngestModeSetCmd.Flags().StringVar(&ingestModeSetFlags.Mode, "mode", "", "stream 或 pull")
	collectorIngestModeSetCmd.Flags().StringVar(&ingestModeSetFlags.Executor, "executor", "", "scf 或 local；留空时 stream 用 local，pull 用 scf")
	collectorIngestModeSetCmd.Flags().BoolVar(&ingestModeSetFlags.Force, "force", false, "旧执行端失联时不等它确认")
	collectorIngestModeSetCmd.Flags().BoolVar(&ingestModeSetFlags.Yes, "yes", false, "确认切换")
	collectorIngestModeSetCmd.Flags().DurationVar(&ingestModeSetFlags.Wait, "wait", 3*time.Minute, "等待交接计划生成并打印生效周期的最长时间；0 表示不等")
}

func getIngestMode(ctx context.Context, invoke func(context.Context, string, string, any, any) error, spaceID, datasetID string) (*collectorpb.GetIngestModeRsp, error) {
	rsp := &collectorpb.GetIngestModeRsp{}
	if err := invoke(ctx, "trpc.moox.collector.CollectMgr", "GetIngestMode", &collectorpb.GetIngestModeReq{SpaceId: spaceID, DatasetId: datasetID}, rsp); err != nil {
		return nil, fmt.Errorf("get ingest mode: %w", err)
	}
	return rsp, collectorRetError("get ingest mode", rsp.GetRetInfo())
}

func ingestModeView(rsp interface {
	GetModes() []*collectorpb.IngestMode
	GetStream() *collectorpb.StreamWorkerStatus
}) map[string]any {
	modes := make([]map[string]any, 0, len(rsp.GetModes()))
	for _, mode := range rsp.GetModes() {
		item := map[string]any{"dataset_id": mode.GetDatasetId(), "mode": mode.GetMode(), "executor": mode.GetExecutor(), "updated_at": mode.GetUpdatedAt()}
		if len(mode.GetEffectivePeriods()) > 0 {
			item["effective_periods"] = mode.GetEffectivePeriods()
		}
		modes = append(modes, item)
	}
	view := map[string]any{"modes": modes}
	if stream := rsp.GetStream(); stream != nil {
		view["stream"] = map[string]any{"instance_id": stream.GetInstanceId(), "online": stream.GetOnline(), "heartbeat_at": stream.GetHeartbeatAt(),
			"applied_plan": fmt.Sprintf("%d/%s", stream.GetAppliedPlanId(), stream.GetAppliedStage()), "connections": len(stream.GetConns()), "version": stream.GetVersion()}
	} else {
		view["stream"] = "没有 collector-stream 实例的心跳"
	}
	return view
}

func runIngestModeGet(ctx context.Context, out io.Writer, flags ingestModeFlags) error {
	if strings.TrimSpace(flags.SpaceID) == "" {
		return errors.New("--space-id is required")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp, err := getIngestMode(ctx, gateway.Invoke, flags.SpaceID, flags.DatasetID)
	if err != nil {
		return err
	}
	return printJSON(out, ingestModeView(rsp))
}

func runIngestModeSet(ctx context.Context, out io.Writer, flags ingestModeFlags) error {
	if strings.TrimSpace(flags.SpaceID) == "" {
		return errors.New("--space-id is required")
	}
	mode := strings.ToLower(strings.TrimSpace(flags.Mode))
	if mode != "stream" && mode != "pull" {
		return errors.New("--mode 必须是 stream 或 pull")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	if !flags.Yes {
		current, err := getIngestMode(ctx, gateway.Invoke, flags.SpaceID, flags.DatasetID)
		if err != nil {
			return err
		}
		view := ingestModeView(current)
		view["next"] = map[string]any{"mode": mode, "executor": flags.Executor, "force": flags.Force}
		if err := printJSON(out, view); err != nil {
			return err
		}
		return errors.New("确认后加 --yes 执行切换（建议在分钟第 20～40 秒执行）")
	}
	rsp := &collectorpb.SetIngestModeRsp{}
	request := &collectorpb.SetIngestModeReq{SpaceId: flags.SpaceID, DatasetId: flags.DatasetID, Mode: mode, Executor: strings.ToLower(strings.TrimSpace(flags.Executor)), Force: flags.Force}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "SetIngestMode", request, rsp); err != nil {
		return fmt.Errorf("set ingest mode: %w", err)
	}
	if err := collectorRetError("set ingest mode", rsp.GetRetInfo()); err != nil {
		if stream := rsp.GetStream(); stream != nil {
			return fmt.Errorf("%w（collector-stream %s 最近心跳 %s）", err, stream.GetInstanceId(), stream.GetHeartbeatAt())
		}
		return err
	}
	// 等对账（每分钟一轮）生成交接计划，打印每个数据集按频率选用的生效周期 P。
	deadline := time.Now().Add(flags.Wait)
	latest := rsp
	for flags.Wait > 0 && time.Now().Before(deadline) {
		current, err := getIngestMode(ctx, gateway.Invoke, flags.SpaceID, flags.DatasetID)
		if err != nil {
			return err
		}
		pending := false
		for _, item := range current.GetModes() {
			if item.GetMode() == mode && len(item.GetEffectivePeriods()) == 0 {
				pending = true
			}
		}
		view := ingestModeView(current)
		if !pending {
			view["status"] = "交接计划已生成"
			return printJSON(out, view)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	view := ingestModeView(latest)
	view["status"] = "已保存；交接计划尚未出现（数据集的执行端可能没有变化，或对账还没跑到），用 ingest-mode get 查看"
	return printJSON(out, view)
}
