package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/spf13/cobra"
)

type klineRangeFlags struct {
	File      string
	SpaceID   string
	DatasetID string
	SubjectID string
	From      string
	To        string
	Deep      bool
	Force     bool
	JobID     string
}

var (
	klineCheckFlags          klineRangeFlags
	klineBackfillFlags       klineRangeFlags
	klineBackfillStatusFlags klineRangeFlags
	klineBackfillCancelFlags klineRangeFlags
	fetchPlanFlags           klineRangeFlags
)

var collectorKlineCmd = &cobra.Command{Use: "kline", Short: "K 线完整性检查与补采"}

var collectorKlineCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "按采集计划检查时间范围内的 K 线是否齐全，输出缺口",
	Long: "由已生效的采集计划还原每段时间应有的（数据集、标的），读取 Storage 中的 K 线比对，连续缺口合并输出。\n" +
		"--deep 额外校验每根 K 线的字段齐全。时间用 RFC3339（如 2026-10-12T08:00:00Z）。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runKlineCheck(cmd.Context(), cmd.OutOrStdout(), klineCheckFlags)
	},
}

var collectorKlineBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "补采缺失的 K 线（默认补检查出的缺口，--force 强制重拉整个范围）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runKlineBackfill(cmd.Context(), cmd.OutOrStdout(), klineBackfillFlags)
	},
}

var collectorKlineBackfillStatusCmd = &cobra.Command{
	Use:   "backfill-status",
	Short: "查看补采作业进度；不带 --job-id 时列出空间最近的作业",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runKlineBackfillStatus(cmd.Context(), cmd.OutOrStdout(), klineBackfillStatusFlags)
	},
}

var collectorKlineBackfillCancelCmd = &cobra.Command{
	Use:   "backfill-cancel",
	Short: "取消补采作业（运行中的分块等其结束）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runKlineBackfillCancel(cmd.Context(), cmd.OutOrStdout(), klineBackfillCancelFlags)
	},
}

var collectorFetchPlanCmd = &cobra.Command{
	Use:   "fetch-plan",
	Short: "查看空间的采集计划：函数分配、数据集 hash 与变更进度",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runFetchPlan(cmd.Context(), cmd.OutOrStdout(), fetchPlanFlags)
	},
}

var fetchPlanReregisterFlags struct {
	klineRangeFlags
	Yes bool
}

var collectorFetchPlanReregisterCmd = &cobra.Command{
	Use:   "reregister",
	Short: "Storage 数据被清空后，让采集计划重新登记序列快照并重置补采状态",
	Long: "Storage 的数据被整体清空（moox-cli storage reset-view-consumers --reset-all-storage-data）后，已登记的序列快照也没了，\n" +
		"采集函数的周期提交会因“快照缺失”被拒绝；而对账只在数据集标的集合变化时才重新登记。本命令把最近一份已生效计划里的\n" +
		"全部数据集标记为“快照未登记”，下一轮对账（约 1 分钟内）会生成新计划、重新登记快照并选新的生效周期，\n" +
		"1m 数据集约 2 分钟后恢复写入，1h 数据集等下一个整点。同时清掉该空间的补采作业，并把自动补采水位推到现在\n" +
		"（否则会从最早的计划起点开始回补历史）。空间里还有未生效的计划时拒绝，等它生效后再执行。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runFetchPlanReregister(cmd.Context(), cmd.OutOrStdout(), fetchPlanReregisterFlags.klineRangeFlags, fetchPlanReregisterFlags.Yes)
	},
}

func init() {
	collectorCmd.AddCommand(collectorKlineCmd, collectorFetchPlanCmd)
	collectorFetchPlanCmd.AddCommand(collectorFetchPlanReregisterCmd)
	collectorFetchPlanReregisterCmd.Flags().StringVar(&fetchPlanReregisterFlags.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	collectorFetchPlanReregisterCmd.Flags().StringVar(&fetchPlanReregisterFlags.SpaceID, "space-id", "", "空间 ID")
	collectorFetchPlanReregisterCmd.Flags().BoolVar(&fetchPlanReregisterFlags.Yes, "yes", false, "确认清空补采作业并让采集计划重新登记")
	collectorKlineCmd.AddCommand(collectorKlineCheckCmd, collectorKlineBackfillCmd, collectorKlineBackfillStatusCmd, collectorKlineBackfillCancelCmd)
	bindRange := func(cmd *cobra.Command, flags *klineRangeFlags, withRange bool) {
		cmd.Flags().StringVar(&flags.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
		cmd.Flags().StringVar(&flags.SpaceID, "space-id", "", "空间 ID")
		if withRange {
			cmd.Flags().StringVar(&flags.DatasetID, "dataset-id", "", "只处理指定数据集；留空表示空间内全部")
			cmd.Flags().StringVar(&flags.SubjectID, "subject", "", "只处理指定标的")
			cmd.Flags().StringVar(&flags.From, "from", "", "起始时间（含），RFC3339")
			cmd.Flags().StringVar(&flags.To, "to", "", "结束时间（不含），RFC3339")
		}
	}
	bindRange(collectorKlineCheckCmd, &klineCheckFlags, true)
	collectorKlineCheckCmd.Flags().BoolVar(&klineCheckFlags.Deep, "deep", false, "校验每根 K 线的字段齐全")
	bindRange(collectorKlineBackfillCmd, &klineBackfillFlags, true)
	collectorKlineBackfillCmd.Flags().BoolVar(&klineBackfillFlags.Force, "force", false, "不检查缺口，范围内应有的 K 线全部重拉；范围可以早于采集计划的生效点（沿用最早一段的标的）")
	bindRange(collectorKlineBackfillStatusCmd, &klineBackfillStatusFlags, false)
	collectorKlineBackfillStatusCmd.Flags().StringVar(&klineBackfillStatusFlags.JobID, "job-id", "", "补采作业 ID")
	bindRange(collectorKlineBackfillCancelCmd, &klineBackfillCancelFlags, false)
	collectorKlineBackfillCancelCmd.Flags().StringVar(&klineBackfillCancelFlags.JobID, "job-id", "", "补采作业 ID")
	bindRange(collectorFetchPlanCmd, &fetchPlanFlags, false)
}

// parseKlineTime 接受 RFC3339，以及不带时区的 2006-01-02T15:04 与 2006-01-02（按 UTC）。
func parseKlineTime(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("--%s is required", name)
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.UTC().Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("--%s must be RFC3339 (for example 2026-10-12T08:00:00Z)", name)
}

func (flags klineRangeFlags) rangeStrings() (string, string, error) {
	if strings.TrimSpace(flags.SpaceID) == "" {
		return "", "", errors.New("--space-id is required")
	}
	from, err := parseKlineTime("from", flags.From)
	if err != nil {
		return "", "", err
	}
	to, err := parseKlineTime("to", flags.To)
	if err != nil {
		return "", "", err
	}
	return from, to, nil
}

func printJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func collectorRetError(action string, ret *collectorpb.RetInfo) error {
	if ret.GetCode() != collectorpb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s (code %d)", action, ret.GetMsg(), ret.GetCode())
	}
	return nil
}

func runKlineCheck(ctx context.Context, out io.Writer, flags klineRangeFlags) error {
	from, to, err := flags.rangeStrings()
	if err != nil {
		return err
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &collectorpb.CheckKlineRsp{}
	req := &collectorpb.CheckKlineReq{SpaceId: flags.SpaceID, DatasetId: flags.DatasetID, SubjectId: flags.SubjectID, From: from, To: to, Deep: flags.Deep}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "CheckKline", req, rsp); err != nil {
		return fmt.Errorf("check kline: %w", err)
	}
	if err := collectorRetError("check kline", rsp.GetRetInfo()); err != nil {
		return err
	}
	type gap struct {
		DatasetID string `json:"dataset_id"`
		Frequency string `json:"frequency"`
		SubjectID string `json:"subject_id"`
		From      string `json:"from"`
		To        string `json:"to"`
		Missing   int32  `json:"missing"`
	}
	result := struct {
		Status    string `json:"status"`
		Expected  int32  `json:"expected"`
		Missing   int32  `json:"missing"`
		Truncated bool   `json:"truncated,omitempty"`
		Gaps      []gap  `json:"gaps"`
	}{Status: "complete", Expected: rsp.GetExpected(), Missing: rsp.GetMissing(), Truncated: rsp.GetTruncated(), Gaps: []gap{}}
	if rsp.GetMissing() > 0 {
		result.Status = "gaps"
	}
	for _, item := range rsp.GetGaps() {
		result.Gaps = append(result.Gaps, gap{DatasetID: item.GetDatasetId(), Frequency: item.GetFrequency(), SubjectID: item.GetSubjectId(), From: item.GetFrom(), To: item.GetTo(), Missing: item.GetMissing()})
	}
	return printJSON(out, result)
}

func runKlineBackfill(ctx context.Context, out io.Writer, flags klineRangeFlags) error {
	from, to, err := flags.rangeStrings()
	if err != nil {
		return err
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &collectorpb.SubmitKlineBackfillRsp{}
	req := &collectorpb.SubmitKlineBackfillReq{SpaceId: flags.SpaceID, DatasetId: flags.DatasetID, SubjectId: flags.SubjectID, From: from, To: to, Force: flags.Force}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "SubmitKlineBackfill", req, rsp); err != nil {
		return fmt.Errorf("submit kline backfill: %w", err)
	}
	if err := collectorRetError("submit kline backfill", rsp.GetRetInfo()); err != nil {
		return err
	}
	status := "submitted"
	if rsp.GetJobId() == "" {
		status = "no_gap"
	}
	return printJSON(out, map[string]any{"status": status, "job_id": rsp.GetJobId(), "chunks": rsp.GetChunks()})
}

func klineJobMap(job *collectorpb.KlineBackfillJob) map[string]any {
	return map[string]any{
		"job_id": job.GetJobId(), "space_id": job.GetSpaceId(), "source": job.GetSource(), "status": job.GetStatus(), "last_error": job.GetLastError(),
		"created_at": job.GetCreatedAt(), "updated_at": job.GetUpdatedAt(), "request": json.RawMessage(job.GetRequest()),
		"chunks": map[string]int32{"pending": job.GetPending(), "running": job.GetRunning(), "succeeded": job.GetSucceeded(), "failed": job.GetFailed(), "canceled": job.GetCanceled()},
	}
}

func runKlineBackfillStatus(ctx context.Context, out io.Writer, flags klineRangeFlags) error {
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	if strings.TrimSpace(flags.SpaceID) == "" {
		return errors.New("--space-id is required")
	}
	if strings.TrimSpace(flags.JobID) == "" {
		rsp := &collectorpb.ListKlineBackfillsRsp{}
		if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "ListKlineBackfills", &collectorpb.ListKlineBackfillsReq{SpaceId: flags.SpaceID}, rsp); err != nil {
			return fmt.Errorf("list kline backfills: %w", err)
		}
		if err := collectorRetError("list kline backfills", rsp.GetRetInfo()); err != nil {
			return err
		}
		jobs := make([]map[string]any, 0, len(rsp.GetItems()))
		for _, job := range rsp.GetItems() {
			jobs = append(jobs, klineJobMap(job))
		}
		return printJSON(out, map[string]any{"jobs": jobs, "total": rsp.GetPage().GetTotal()})
	}
	rsp := &collectorpb.GetKlineBackfillRsp{}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "GetKlineBackfill", &collectorpb.GetKlineBackfillReq{JobId: flags.JobID, SpaceId: flags.SpaceID}, rsp); err != nil {
		return fmt.Errorf("get kline backfill: %w", err)
	}
	if err := collectorRetError("get kline backfill", rsp.GetRetInfo()); err != nil {
		return err
	}
	failed := make([]map[string]any, 0, len(rsp.GetFailedChunks()))
	for _, chunk := range rsp.GetFailedChunks() {
		failed = append(failed, map[string]any{"dataset_id": chunk.GetDatasetId(), "frequency": chunk.GetFrequency(), "subject_id": chunk.GetSubjectId(), "from": chunk.GetFrom(), "to": chunk.GetTo(), "attempts": chunk.GetAttempts(), "last_error": chunk.GetLastError()})
	}
	result := klineJobMap(rsp.GetJob())
	result["failed_chunks"] = failed
	return printJSON(out, result)
}

func runKlineBackfillCancel(ctx context.Context, out io.Writer, flags klineRangeFlags) error {
	if strings.TrimSpace(flags.JobID) == "" || strings.TrimSpace(flags.SpaceID) == "" {
		return errors.New("--job-id and --space-id are required")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &collectorpb.CancelKlineBackfillRsp{}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "CancelKlineBackfill", &collectorpb.CancelKlineBackfillReq{JobId: flags.JobID, SpaceId: flags.SpaceID}, rsp); err != nil {
		return fmt.Errorf("cancel kline backfill: %w", err)
	}
	if err := collectorRetError("cancel kline backfill", rsp.GetRetInfo()); err != nil {
		return err
	}
	return printJSON(out, map[string]any{"status": "canceled", "job_id": flags.JobID})
}

func runFetchPlan(ctx context.Context, out io.Writer, flags klineRangeFlags) error {
	if strings.TrimSpace(flags.SpaceID) == "" {
		return errors.New("--space-id is required")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &collectorpb.GetFetchPlansRsp{}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "GetFetchPlans", &collectorpb.GetFetchPlansReq{SpaceId: flags.SpaceID}, rsp); err != nil {
		return fmt.Errorf("get fetch plans: %w", err)
	}
	if err := collectorRetError("get fetch plans", rsp.GetRetInfo()); err != nil {
		return err
	}
	summary := func(plan *collectorpb.FetchPlanSummary) any {
		if plan == nil {
			return nil
		}
		return map[string]any{"id": plan.GetId(), "status": plan.GetStatus(), "last_error": plan.GetLastError(), "created_at": plan.GetCreatedAt(), "updated_at": plan.GetUpdatedAt()}
	}
	datasets := make([]map[string]any, 0, len(rsp.GetDatasets()))
	for _, dataset := range rsp.GetDatasets() {
		datasets = append(datasets, map[string]any{"dataset_id": dataset.GetDatasetId(), "frequency": dataset.GetFrequency(), "hash": dataset.GetHash(), "subjects": dataset.GetSubjectCount(), "effective_period": dataset.GetEffectivePeriod()})
	}
	functions := make([]map[string]any, 0, len(rsp.GetFunctions()))
	for _, function := range rsp.GetFunctions() {
		functions = append(functions, map[string]any{"name": function.GetName(), "region": function.GetRegion(), "market": function.GetMarket(), "cron": function.GetCron(), "subjects": function.GetSubjectCount(), "datasets": function.GetDatasets(), "state": function.GetState()})
	}
	return printJSON(out, map[string]any{"open": summary(rsp.GetOpen()), "applied": summary(rsp.GetApplied()), "datasets": datasets, "functions": functions})
}

// runFetchPlanReregister 调用 ReregisterFetchPlan；没有 --yes 时只提示，不动任何东西。
func runFetchPlanReregister(ctx context.Context, out io.Writer, flags klineRangeFlags, yes bool) error {
	if strings.TrimSpace(flags.SpaceID) == "" {
		return errors.New("--space-id is required")
	}
	if !yes {
		return errors.New("该命令会清空空间的补采作业并让采集计划重新登记序列快照；确认后加 --yes 重新执行")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &collectorpb.ReregisterFetchPlanRsp{}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "ReregisterFetchPlan", &collectorpb.ReregisterFetchPlanReq{SpaceId: flags.SpaceID}, rsp); err != nil {
		return fmt.Errorf("reregister fetch plan: %w", err)
	}
	if err := collectorRetError("reregister fetch plan", rsp.GetRetInfo()); err != nil {
		return err
	}
	return printJSON(out, map[string]any{"status": "marked", "space_id": flags.SpaceID, "datasets": rsp.GetDatasets(), "backfill_jobs_removed": rsp.GetBackfillJobs()})
}
