package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/spf13/cobra"
)

type resampleFlags struct {
	klineRangeFlags
	TaskID    string
	RequestID string
	Subjects  []string
	Status    string
	Limit     uint32
	Wait      bool
	Timeout   time.Duration
}

var (
	resampleBackfillFlags resampleFlags
	resampleJobsFlags     resampleFlags
	resampleCancelFlags   resampleFlags
)

var collectorResampleCmd = &cobra.Command{Use: "resample", Short: "K 线合成：历史合成作业"}

var collectorResampleBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "提交历史合成作业：把时间范围内的目标 K 线按源数据重新合成",
	Long: "对合成任务提交一个历史合成作业。范围按 UTC 解析并向外对齐到目标桶（交易时段市场只含交易时段内的桶），最多 10080 个桶。\n" +
		"作业按桶推进、可取消，源数据不完整的标的会跳过并计数（补采源数据后补采完成事件会自动再触发重算，也可对同一范围再提交一次）。\n" +
		"同一个 --request-id 重复提交返回已有作业；不指定时每次生成新的。时间用 RFC3339 或 2026-10-12、2026-10-12T08:00（UTC）。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runResampleBackfill(cmd.Context(), cmd.OutOrStdout(), resampleBackfillFlags)
	},
}

var collectorResampleJobsCmd = &cobra.Command{
	Use:   "jobs",
	Short: "查看历史合成作业；带 --job-id 查看单个作业，否则按任务、状态列出最近的作业",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runResampleJobs(cmd.Context(), cmd.OutOrStdout(), resampleJobsFlags)
	},
}

var collectorResampleCancelCmd = &cobra.Command{
	Use:   "cancel",
	Short: "取消排队或运行中的历史合成作业（已写入的保留）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runResampleCancel(cmd.Context(), cmd.OutOrStdout(), resampleCancelFlags)
	},
}

func init() {
	collectorCmd.AddCommand(collectorResampleCmd)
	collectorResampleCmd.AddCommand(collectorResampleBackfillCmd, collectorResampleJobsCmd, collectorResampleCancelCmd)
	bind := func(cmd *cobra.Command, flags *resampleFlags) {
		cmd.Flags().StringVar(&flags.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
		cmd.Flags().StringVar(&flags.SpaceID, "space-id", "", "空间 ID")
	}
	bind(collectorResampleBackfillCmd, &resampleBackfillFlags)
	collectorResampleBackfillCmd.Flags().StringVar(&resampleBackfillFlags.TaskID, "task-id", "", "合成任务 ID")
	collectorResampleBackfillCmd.Flags().StringVar(&resampleBackfillFlags.From, "from", "", "起始时间（含）")
	collectorResampleBackfillCmd.Flags().StringVar(&resampleBackfillFlags.To, "to", "", "结束时间（不含）")
	collectorResampleBackfillCmd.Flags().StringSliceVar(&resampleBackfillFlags.Subjects, "subjects", nil, "只合成这些标的（逗号分隔）；不指定表示全部")
	collectorResampleBackfillCmd.Flags().StringVar(&resampleBackfillFlags.RequestID, "request-id", "", "幂等键；同一个任务内重复提交返回已有作业")
	collectorResampleBackfillCmd.Flags().BoolVar(&resampleBackfillFlags.Wait, "wait", false, "等作业结束，输出写入数、跳过数与跳过样例")
	collectorResampleBackfillCmd.Flags().DurationVar(&resampleBackfillFlags.Timeout, "timeout", 30*time.Minute, "--wait 的最长等待时间")
	bind(collectorResampleJobsCmd, &resampleJobsFlags)
	collectorResampleJobsCmd.Flags().StringVar(&resampleJobsFlags.TaskID, "task-id", "", "只看该合成任务的作业")
	collectorResampleJobsCmd.Flags().StringVar(&resampleJobsFlags.JobID, "job-id", "", "作业 ID")
	collectorResampleJobsCmd.Flags().StringVar(&resampleJobsFlags.Status, "status", "", "只看该状态：queued、running、syncing、succeeded、failed、cancelled")
	collectorResampleJobsCmd.Flags().Uint32Var(&resampleJobsFlags.Limit, "limit", 50, "最多列出多少个")
	bind(collectorResampleCancelCmd, &resampleCancelFlags)
	collectorResampleCancelCmd.Flags().StringVar(&resampleCancelFlags.JobID, "job-id", "", "作业 ID")
}

func resampleJobMap(job *collectorpb.ResampleJob) map[string]any {
	if job == nil {
		return nil
	}
	return map[string]any{
		"job_id": job.GetJobId(), "task_id": job.GetTaskId(), "request_id": job.GetRequestId(), "origin": job.GetOrigin(), "status": job.GetStatus(),
		"subjects": job.GetSubjects(), "start": job.GetStart(), "end": job.GetEnd(), "next_bucket": job.GetNextBucket(), "not_before": job.GetNotBefore(),
		"buckets_total": job.GetTotalBuckets(), "buckets_done": job.GetDoneBuckets(), "written": job.GetWritten(), "skipped": job.GetSkipped(),
		"skipped_samples": job.GetSkippedSamples(), "last_error": job.GetLastError(), "created_at": job.GetCreatedAt(), "updated_at": job.GetUpdatedAt(),
	}
}

func newResampleRequestID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("cli-%d", time.Now().UnixNano())
	}
	return "cli-" + hex.EncodeToString(buf)
}

func runResampleBackfill(ctx context.Context, out io.Writer, flags resampleFlags) error {
	if strings.TrimSpace(flags.SpaceID) == "" || strings.TrimSpace(flags.TaskID) == "" {
		return errors.New("--space-id and --task-id are required")
	}
	from, err := parseKlineTime("from", flags.From)
	if err != nil {
		return err
	}
	to, err := parseKlineTime("to", flags.To)
	if err != nil {
		return err
	}
	requestID := strings.TrimSpace(flags.RequestID)
	if requestID == "" {
		requestID = newResampleRequestID()
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &collectorpb.SubmitKlineResampleJobRsp{}
	req := &collectorpb.SubmitKlineResampleJobReq{SpaceId: flags.SpaceID, TaskId: flags.TaskID, RequestId: requestID, Start: from, End: to, Subjects: flags.Subjects}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "SubmitKlineResampleJob", req, rsp); err != nil {
		return fmt.Errorf("submit resample job: %w", err)
	}
	if err := collectorRetError("submit resample job", rsp.GetRetInfo()); err != nil {
		return err
	}
	job := rsp.GetJob()
	if !flags.Wait {
		return printJSON(out, map[string]any{"status": "submitted", "created": rsp.GetCreated(), "job": resampleJobMap(job)})
	}
	deadline := time.Now().Add(flags.Timeout)
	for {
		get := &collectorpb.GetKlineResampleJobRsp{}
		if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "GetKlineResampleJob", &collectorpb.GetKlineResampleJobReq{SpaceId: flags.SpaceID, JobId: job.GetJobId()}, get); err != nil {
			return fmt.Errorf("get resample job: %w", err)
		}
		if err := collectorRetError("get resample job", get.GetRetInfo()); err != nil {
			return err
		}
		job = get.GetJob()
		switch job.GetStatus() {
		case "succeeded":
			return printJSON(out, map[string]any{"status": "succeeded", "job": resampleJobMap(job)})
		case "failed", "cancelled":
			_ = printJSON(out, map[string]any{"status": job.GetStatus(), "job": resampleJobMap(job)})
			return fmt.Errorf("resample job %s %s: %s", job.GetJobId(), job.GetStatus(), job.GetLastError())
		}
		if time.Now().After(deadline) {
			_ = printJSON(out, map[string]any{"status": "timeout", "job": resampleJobMap(job)})
			return fmt.Errorf("resample job %s did not finish within %s (status %s)", job.GetJobId(), flags.Timeout, job.GetStatus())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func runResampleJobs(ctx context.Context, out io.Writer, flags resampleFlags) error {
	if strings.TrimSpace(flags.SpaceID) == "" {
		return errors.New("--space-id is required")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	if strings.TrimSpace(flags.JobID) != "" {
		rsp := &collectorpb.GetKlineResampleJobRsp{}
		if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "GetKlineResampleJob", &collectorpb.GetKlineResampleJobReq{SpaceId: flags.SpaceID, JobId: flags.JobID}, rsp); err != nil {
			return fmt.Errorf("get resample job: %w", err)
		}
		if err := collectorRetError("get resample job", rsp.GetRetInfo()); err != nil {
			return err
		}
		return printJSON(out, resampleJobMap(rsp.GetJob()))
	}
	rsp := &collectorpb.ListKlineResampleJobsRsp{}
	req := &collectorpb.ListKlineResampleJobsReq{SpaceId: flags.SpaceID, TaskId: flags.TaskID, Status: flags.Status, Limit: flags.Limit}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "ListKlineResampleJobs", req, rsp); err != nil {
		return fmt.Errorf("list resample jobs: %w", err)
	}
	if err := collectorRetError("list resample jobs", rsp.GetRetInfo()); err != nil {
		return err
	}
	jobs := make([]map[string]any, 0, len(rsp.GetJobs()))
	for _, job := range rsp.GetJobs() {
		jobs = append(jobs, resampleJobMap(job))
	}
	return printJSON(out, map[string]any{"jobs": jobs})
}

func runResampleCancel(ctx context.Context, out io.Writer, flags resampleFlags) error {
	if strings.TrimSpace(flags.SpaceID) == "" || strings.TrimSpace(flags.JobID) == "" {
		return errors.New("--space-id and --job-id are required")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	rsp := &collectorpb.CancelKlineResampleJobRsp{}
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "CancelKlineResampleJob", &collectorpb.CancelKlineResampleJobReq{SpaceId: flags.SpaceID, JobId: flags.JobID}, rsp); err != nil {
		return fmt.Errorf("cancel resample job: %w", err)
	}
	if err := collectorRetError("cancel resample job", rsp.GetRetInfo()); err != nil {
		return err
	}
	return printJSON(out, map[string]any{"status": "cancelled", "job": resampleJobMap(rsp.GetJob())})
}
