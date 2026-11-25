package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/spf13/cobra"
)

var historySeedSkipFlags klineRangeFlags

var collectorHistorySeedCmd = &cobra.Command{Use: "history-seed", Short: "首次接入的历史补齐：手动放行"}

var collectorHistorySeedSkipCmd = &cobra.Command{
	Use:   "skip",
	Short: "不等历史补齐了，立即放行下游：取消剩余分块，状态记为已手动放行，已经补进去的数据保留",
	Long: "首次接入的数据集会先自动补历史，补齐期间读它的因子不计算、策略不求值。不想等时用本命令放行：\n" +
		"取消剩余的补齐分块、状态记 skipped，已经写入的历史保留，因子与策略随后恢复。\n" +
		"--dataset-id 留空表示空间内全部补齐中的数据集。放行后缺的那段历史不会自动补，需要时用 collector kline backfill 手动补采。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runHistorySeedSkip(cmd.Context(), cmd.OutOrStdout(), historySeedSkipFlags)
	},
}

func init() {
	collectorCmd.AddCommand(collectorHistorySeedCmd)
	collectorHistorySeedCmd.AddCommand(collectorHistorySeedSkipCmd)
	collectorHistorySeedSkipCmd.Flags().StringVar(&historySeedSkipFlags.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	collectorHistorySeedSkipCmd.Flags().StringVar(&historySeedSkipFlags.SpaceID, "space-id", "", "空间 ID")
	collectorHistorySeedSkipCmd.Flags().StringVar(&historySeedSkipFlags.DatasetID, "dataset-id", "", "只放行指定数据集；留空表示空间内全部补齐中的数据集")
}

func runHistorySeedSkip(ctx context.Context, out io.Writer, flags klineRangeFlags) error {
	if strings.TrimSpace(flags.SpaceID) == "" {
		return errors.New("--space-id is required")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &collectorpb.SkipHistorySeedRsp{}
	req := &collectorpb.SkipHistorySeedReq{SpaceId: flags.SpaceID, DatasetId: strings.TrimSpace(flags.DatasetID)}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "SkipHistorySeed", req, rsp); err != nil {
		return fmt.Errorf("skip history seed: %w", err)
	}
	if err := collectorRetError("skip history seed", rsp.GetRetInfo()); err != nil {
		return err
	}
	return printJSON(out, map[string]any{"status": "skipped", "space_id": flags.SpaceID, "dataset_id": req.GetDatasetId(), "skipped": rsp.GetSkipped()})
}

// historySeedMaps 把 GetCollectorStatus 里的历史补齐记录转成输出项：状态、进度、预计剩余、失败分块。
func historySeedMaps(seeds []*collectorpb.HistorySeed) []map[string]any {
	out := make([]map[string]any, 0, len(seeds))
	for _, seed := range seeds {
		item := map[string]any{
			"dataset_id": seed.GetDatasetId(), "frequency": seed.GetFrequency(), "status": seed.GetStatus(),
			"progress":    fmt.Sprintf("%d/%d", seed.GetDoneChunks(), seed.GetTotalChunks()),
			"done_chunks": seed.GetDoneChunks(), "total_chunks": seed.GetTotalChunks(), "failed_chunks": seed.GetFailedChunks(),
			"target_from": seed.GetTargetFrom(), "started_at": seed.GetStartedAt(), "finished_at": seed.GetFinishedAt(),
		}
		if seed.GetStatus() == "running" {
			item["eta_seconds"] = seed.GetEtaSeconds()
		}
		if note := seed.GetNote(); note != "" {
			item["note"] = note
		}
		if seed.GetFailedChunks() > 0 {
			item["hint"] = "有失败分块：用 collector kline check 查缺口，再用 collector kline backfill 手动补采"
		}
		out = append(out, item)
	}
	return out
}

// historySeedingRunning 返回补齐中的数据集数量。
func historySeedingRunning(seeds []*collectorpb.HistorySeed) int {
	running := 0
	for _, seed := range seeds {
		if seed.GetStatus() == "running" {
			running++
		}
	}
	return running
}
