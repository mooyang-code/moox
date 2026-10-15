package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

// KlineOps 是检查与补采的后端实现。
type KlineOps interface {
	Check(ctx context.Context, request klinecheck.Request) (*klinecheck.Result, error)
	Submit(ctx context.Context, request klinecheck.JobRequest, source string) (string, int, error)
	Status(ctx context.Context, jobID string) (*klinecheck.JobStatus, error)
	List(ctx context.Context, spaceID string, filter store.BackfillJobFilter, page, size int) ([]klinecheck.JobStatus, int64, error)
	Cancel(ctx context.Context, jobID string) error
	Retry(ctx context.Context, jobID string) (int, error)
	ConfirmHistory(ctx context.Context, spaceID, datasetID, frequency string) (string, int, error)
	ClearAbsences(ctx context.Context, spaceID, datasetID, frequency string) (int64, error)
}

// SetKlineOps 注入检查与补采实现。
func (s *Service) SetKlineOps(ops KlineOps) { s.klineOps = ops }

const klineOpsTimeout = 5 * time.Minute

func parseRangeTime(name, value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("%s is required", name)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339", name)
	}
	return parsed.UTC(), nil
}

func parseRange(from, to string) (time.Time, time.Time, error) {
	start, err := parseRangeTime("from", from)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := parseRangeTime("to", to)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("from must be before to")
	}
	return start, end, nil
}

func (s *Service) CheckKline(ctx context.Context, req *pb.CheckKlineReq) (*pb.CheckKlineRsp, error) {
	if s.klineOps == nil {
		return &pb.CheckKlineRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline check is not configured")}, nil
	}
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.CheckKlineRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	from, to, err := parseRange(req.GetFrom(), req.GetTo())
	if err != nil {
		return &pb.CheckKlineRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, klineOpsTimeout)
	defer cancel()
	result, err := s.klineOps.Check(ctx, klinecheck.Request{SpaceID: strings.TrimSpace(req.GetSpaceId()), DatasetID: strings.TrimSpace(req.GetDatasetId()), SubjectID: strings.TrimSpace(req.GetSubjectId()), From: from, To: to, Deep: req.GetDeep()})
	if err != nil {
		log.ErrorContextf(ctx, "check kline failed: %v", err)
		return &pb.CheckKlineRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.CheckKlineRsp{RetInfo: retOK(), Expected: int32(result.Expected), Missing: int32(result.Missing), Absent: int32(result.Absent), Truncated: result.Truncated}
	for _, gap := range result.Gaps {
		rsp.Gaps = append(rsp.Gaps, &pb.KlineGap{DatasetId: gap.DatasetID, Frequency: gap.Frequency, SubjectId: gap.SubjectID, From: gap.From.Format(time.RFC3339), To: gap.To.Format(time.RFC3339), Missing: int32(gap.Missing)})
	}
	return rsp, nil
}

func (s *Service) SubmitKlineBackfill(ctx context.Context, req *pb.SubmitKlineBackfillReq) (*pb.SubmitKlineBackfillRsp, error) {
	if s.klineOps == nil {
		return &pb.SubmitKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline backfill is not configured")}, nil
	}
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.SubmitKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	from, to, err := parseRange(req.GetFrom(), req.GetTo())
	if err != nil {
		return &pb.SubmitKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, klineOpsTimeout)
	defer cancel()
	jobID, chunks, err := s.klineOps.Submit(ctx, klinecheck.JobRequest{SpaceID: strings.TrimSpace(req.GetSpaceId()), DatasetID: strings.TrimSpace(req.GetDatasetId()), SubjectID: strings.TrimSpace(req.GetSubjectId()), From: from, To: to, Force: req.GetForce()}, store.BackfillSourceManual)
	if err != nil {
		log.ErrorContextf(ctx, "submit kline backfill failed: %v", err)
		return &pb.SubmitKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.SubmitKlineBackfillRsp{RetInfo: retOK(), JobId: jobID, Chunks: int32(chunks)}, nil
}

func jobToPB(status klinecheck.JobStatus) *pb.KlineBackfillJob {
	job := status.Job
	return &pb.KlineBackfillJob{
		JobId: job.JobID, SpaceId: job.SpaceID, Source: job.Source, Kind: job.Kind, DatasetId: job.DatasetID, Frequency: job.Frequency, SubjectId: job.SubjectID,
		From: job.From.UTC().Format(time.RFC3339), To: job.To.UTC().Format(time.RFC3339), Status: job.Status, LastError: job.LastError,
		CreatedAt: job.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: job.UpdatedAt.UTC().Format(time.RFC3339),
		Pending: int32(status.Counts[store.BackfillPending]), Running: int32(status.Counts[store.BackfillRunning]), Succeeded: int32(status.Counts[store.BackfillSucceeded]),
		Failed: int32(status.Counts[store.BackfillFailed]), Canceled: int32(status.Counts[store.BackfillCanceled]),
	}
}

func (s *Service) GetKlineBackfill(ctx context.Context, req *pb.GetKlineBackfillReq) (*pb.GetKlineBackfillRsp, error) {
	if s.klineOps == nil {
		return &pb.GetKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline backfill is not configured")}, nil
	}
	if req == nil || strings.TrimSpace(req.GetJobId()) == "" || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.GetKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_id and space_id are required")}, nil
	}
	status, err := s.klineOps.Status(ctx, strings.TrimSpace(req.GetJobId()))
	if err != nil || status.Job.SpaceID != strings.TrimSpace(req.GetSpaceId()) {
		// 作业不属于请求的空间时与不存在一样处理，避免跨空间读取。
		return &pb.GetKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "补采作业不存在")}, nil
	}
	rsp := &pb.GetKlineBackfillRsp{RetInfo: retOK(), Job: jobToPB(*status)}
	for _, chunk := range status.Failed {
		rsp.FailedChunks = append(rsp.FailedChunks, &pb.KlineBackfillChunk{
			DatasetId: chunk.DatasetID, Frequency: chunk.Frequency, SubjectId: chunk.SubjectID, From: chunk.From.UTC().Format(time.RFC3339), To: chunk.To.UTC().Format(time.RFC3339),
			Status: chunk.Status, Attempts: int32(chunk.Attempts), LastError: chunk.LastError, Rows: int32(chunk.Rows),
		})
	}
	return rsp, nil
}

func (s *Service) ListKlineBackfills(ctx context.Context, req *pb.ListKlineBackfillsReq) (*pb.ListKlineBackfillsRsp, error) {
	if s.klineOps == nil {
		return &pb.ListKlineBackfillsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline backfill is not configured")}, nil
	}
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ListKlineBackfillsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	page, size := int(req.GetPage().GetPage()), int(req.GetPage().GetSize())
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	source := strings.TrimSpace(req.GetSource())
	if source != "" && source != store.BackfillSourceManual && source != store.BackfillSourceAuto {
		return &pb.ListKlineBackfillsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "source must be manual or auto")}, nil
	}
	filter := store.BackfillJobFilter{Source: source, Attention: req.GetAttention(), DatasetID: strings.TrimSpace(req.GetDatasetId())}
	items, total, err := s.klineOps.List(ctx, strings.TrimSpace(req.GetSpaceId()), filter, page, size)
	if err != nil {
		return &pb.ListKlineBackfillsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.ListKlineBackfillsRsp{RetInfo: retOK(), Page: &pb.PageResult{Page: uint32(page), Size: uint32(size), Total: uint32(total), HasMore: int64(page*size) < total}}
	for _, item := range items {
		rsp.Items = append(rsp.Items, jobToPB(item))
	}
	return rsp, nil
}

func (s *Service) CancelKlineBackfill(ctx context.Context, req *pb.CancelKlineBackfillReq) (*pb.CancelKlineBackfillRsp, error) {
	if s.klineOps == nil {
		return &pb.CancelKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline backfill is not configured")}, nil
	}
	if req == nil || strings.TrimSpace(req.GetJobId()) == "" || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.CancelKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_id and space_id are required")}, nil
	}
	status, err := s.klineOps.Status(ctx, strings.TrimSpace(req.GetJobId()))
	if err != nil || status.Job.SpaceID != strings.TrimSpace(req.GetSpaceId()) {
		return &pb.CancelKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "补采作业不存在")}, nil
	}
	if err := s.klineOps.Cancel(ctx, strings.TrimSpace(req.GetJobId())); err != nil {
		return &pb.CancelKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.CancelKlineBackfillRsp{RetInfo: retOK()}, nil
}

// RetryKlineBackfill 把已失败作业的失败分块退回待执行。
func (s *Service) RetryKlineBackfill(ctx context.Context, req *pb.RetryKlineBackfillReq) (*pb.RetryKlineBackfillRsp, error) {
	if s.klineOps == nil {
		return &pb.RetryKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline backfill is not configured")}, nil
	}
	if req == nil || strings.TrimSpace(req.GetJobId()) == "" || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.RetryKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_id and space_id are required")}, nil
	}
	status, err := s.klineOps.Status(ctx, strings.TrimSpace(req.GetJobId()))
	if err != nil || status.Job.SpaceID != strings.TrimSpace(req.GetSpaceId()) {
		return &pb.RetryKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "补采作业不存在")}, nil
	}
	chunks, err := s.klineOps.Retry(ctx, strings.TrimSpace(req.GetJobId()))
	if errors.Is(err, store.ErrJobNotRetryable) {
		return &pb.RetryKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "只有已结束且有失败分块的作业可以重试")}, nil
	}
	if err != nil {
		return &pb.RetryKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.RetryKlineBackfillRsp{RetInfo: retOK(), Chunks: int32(chunks)}, nil
}

func datasetScope(spaceID, datasetID, frequency string) (string, string, string, error) {
	spaceID, datasetID, frequency = strings.TrimSpace(spaceID), strings.TrimSpace(datasetID), strings.TrimSpace(frequency)
	if spaceID == "" || datasetID == "" || frequency == "" {
		return "", "", "", fmt.Errorf("space_id, dataset_id and frequency are required")
	}
	return spaceID, datasetID, frequency, nil
}

// ConfirmHistoryGaps 按最近一次历史扫描记下的缺口建立补采作业。
func (s *Service) ConfirmHistoryGaps(ctx context.Context, req *pb.ConfirmHistoryGapsReq) (*pb.ConfirmHistoryGapsRsp, error) {
	if s.klineOps == nil {
		return &pb.ConfirmHistoryGapsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline backfill is not configured")}, nil
	}
	spaceID, datasetID, frequency, err := datasetScope(req.GetSpaceId(), req.GetDatasetId(), req.GetFrequency())
	if err != nil {
		return &pb.ConfirmHistoryGapsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	jobID, chunks, err := s.klineOps.ConfirmHistory(ctx, spaceID, datasetID, frequency)
	if err != nil {
		log.ErrorContextf(ctx, "confirm history gaps failed space=%s dataset=%s|%s: %v", spaceID, datasetID, frequency, err)
		return &pb.ConfirmHistoryGapsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.ConfirmHistoryGapsRsp{RetInfo: retOK(), JobId: jobID, Chunks: int32(chunks)}, nil
}

// ClearSourceAbsences 清掉一个数据集已确认的行情源缺失，并让历史缺口扫描重新进行。
func (s *Service) ClearSourceAbsences(ctx context.Context, req *pb.ClearSourceAbsencesReq) (*pb.ClearSourceAbsencesRsp, error) {
	if s.klineOps == nil {
		return &pb.ClearSourceAbsencesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline backfill is not configured")}, nil
	}
	spaceID, datasetID, frequency, err := datasetScope(req.GetSpaceId(), req.GetDatasetId(), req.GetFrequency())
	if err != nil {
		return &pb.ClearSourceAbsencesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	cleared, err := s.klineOps.ClearAbsences(ctx, spaceID, datasetID, frequency)
	if err != nil {
		return &pb.ClearSourceAbsencesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	log.InfoContextf(ctx, "collector source absences cleared space=%s dataset=%s|%s ranges=%d", spaceID, datasetID, frequency, cleared)
	return &pb.ClearSourceAbsencesRsp{RetInfo: retOK(), Cleared: cleared}, nil
}
