package rpc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

// KlineOps 是检查与补采的后端实现。
type KlineOps interface {
	Check(ctx context.Context, request klinecheck.Request) (*klinecheck.Result, error)
	Submit(ctx context.Context, request klinecheck.JobRequest, source string) (string, int, error)
	Status(ctx context.Context, jobID string) (*klinecheck.JobStatus, error)
	List(ctx context.Context, spaceID string, page, size int) ([]klinecheck.JobStatus, int64, error)
	Cancel(ctx context.Context, jobID string) error
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
	rsp := &pb.CheckKlineRsp{RetInfo: retOK(), Expected: int32(result.Expected), Missing: int32(result.Missing), Truncated: result.Truncated}
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
	jobID, chunks, err := s.klineOps.Submit(ctx, klinecheck.JobRequest{SpaceID: strings.TrimSpace(req.GetSpaceId()), DatasetID: strings.TrimSpace(req.GetDatasetId()), SubjectID: strings.TrimSpace(req.GetSubjectId()), From: from, To: to, Force: req.GetForce()}, "manual")
	if err != nil {
		log.ErrorContextf(ctx, "submit kline backfill failed: %v", err)
		return &pb.SubmitKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.SubmitKlineBackfillRsp{RetInfo: retOK(), JobId: jobID, Chunks: int32(chunks)}, nil
}

func jobToPB(status klinecheck.JobStatus) *pb.KlineBackfillJob {
	return &pb.KlineBackfillJob{
		JobId: status.Job.JobID, SpaceId: status.Job.SpaceID, Source: status.Job.Source, Status: status.Job.Status, LastError: status.Job.LastError, Request: status.Job.Request,
		CreatedAt: status.Job.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: status.Job.UpdatedAt.UTC().Format(time.RFC3339),
		Pending: int32(status.Counts[store.BackfillPending]), Running: int32(status.Counts[store.BackfillRunning]), Succeeded: int32(status.Counts[store.BackfillSucceeded]),
		Failed: int32(status.Counts[store.BackfillFailed]), Canceled: int32(status.Counts[store.BackfillCanceled]),
	}
}

func (s *Service) GetKlineBackfill(ctx context.Context, req *pb.GetKlineBackfillReq) (*pb.GetKlineBackfillRsp, error) {
	if s.klineOps == nil {
		return &pb.GetKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "kline backfill is not configured")}, nil
	}
	if req == nil || strings.TrimSpace(req.GetJobId()) == "" {
		return &pb.GetKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_id is required")}, nil
	}
	status, err := s.klineOps.Status(ctx, strings.TrimSpace(req.GetJobId()))
	if err != nil {
		return &pb.GetKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	rsp := &pb.GetKlineBackfillRsp{RetInfo: retOK(), Job: jobToPB(*status)}
	for _, chunk := range status.Failed {
		rsp.FailedChunks = append(rsp.FailedChunks, &pb.KlineBackfillChunk{
			DatasetId: chunk.DatasetID, Frequency: chunk.Frequency, SubjectId: chunk.SubjectID, From: chunk.From.UTC().Format(time.RFC3339), To: chunk.To.UTC().Format(time.RFC3339),
			Status: chunk.Status, Attempts: int32(chunk.Attempts), LastError: chunk.LastError,
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
	items, total, err := s.klineOps.List(ctx, strings.TrimSpace(req.GetSpaceId()), page, size)
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
	if req == nil || strings.TrimSpace(req.GetJobId()) == "" {
		return &pb.CancelKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_id is required")}, nil
	}
	if err := s.klineOps.Cancel(ctx, strings.TrimSpace(req.GetJobId())); err != nil {
		return &pb.CancelKlineBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.CancelKlineBackfillRsp{RetInfo: retOK()}, nil
}

// GetFetchPlans 返回采集计划的当前状态：分配（函数、地域、标的数、数据集 hash）与最近一份计划的进度。
func (s *Service) GetFetchPlans(ctx context.Context, req *pb.GetFetchPlansReq) (*pb.GetFetchPlansRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.GetFetchPlansRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	repo := s.persistence.FetchPlans()
	applied, err := repo.LatestApplied(ctx, spaceID)
	if err != nil {
		return &pb.GetFetchPlansRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	open, err := repo.Open(ctx, spaceID)
	if err != nil {
		return &pb.GetFetchPlansRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.GetFetchPlansRsp{RetInfo: retOK()}
	summary := func(record *store.FetchPlanRecord) *pb.FetchPlanSummary {
		if record == nil {
			return nil
		}
		return &pb.FetchPlanSummary{Id: record.ID, Status: record.Status, LastError: record.LastError, CreatedAt: record.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: record.UpdatedAt.UTC().Format(time.RFC3339)}
	}
	rsp.Open, rsp.Applied = summary(open), summary(applied)
	if applied == nil && open == nil {
		return rsp, nil
	}
	// 以未生效计划为准展示正在变化的状态，没有则展示已生效的计划。
	source := applied
	if open != nil {
		source = open
	}
	content, err := marketfetch.DecodeFetchPlanContent(source.Plan)
	if err != nil {
		return &pb.GetFetchPlansRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	progress, err := marketfetch.DecodeFetchPlanProgress(source.Progress)
	if err != nil {
		return &pb.GetFetchPlansRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	keys := make([]string, 0, len(content.Datasets))
	for key := range content.Datasets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		change := content.Datasets[key]
		if change.Gone {
			continue
		}
		state := &pb.FetchDatasetState{DatasetId: change.DatasetID, Frequency: change.Frequency, Hash: change.Hash, SubjectCount: int32(change.Count)}
		if change.Changed && change.P > 0 {
			state.EffectivePeriod = time.Unix(change.P, 0).UTC().Format(time.RFC3339)
		}
		rsp.Datasets = append(rsp.Datasets, state)
	}
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		plan := content.TransitionalPlan(function)
		if plan == nil {
			continue
		}
		state := &pb.FetchFunctionState{Name: name, Region: function.Region, Market: function.Market, State: "applied"}
		switch {
		case function.New == nil:
			state.State = "retiring"
			if source.Status == store.FetchPlanApplied {
				continue
			}
		case source.Status != store.FetchPlanApplied && function.Push && !progress.Finalized[name]:
			state.State = "pending"
		}
		final := content.FinalPlan(function)
		if source.Status == store.FetchPlanApplied && final != nil {
			plan = final
		}
		state.Cron = content.TransitionalCron(function)
		if source.Status == store.FetchPlanApplied {
			state.Cron = content.FinalCron(function)
		}
		for _, dataset := range plan.Datasets {
			if len(dataset.Copies) == 0 {
				continue
			}
			last := dataset.Copies[len(dataset.Copies)-1]
			state.SubjectCount += int32(len(last.Subjects))
			state.Datasets = append(state.Datasets, marketfetch.PlanDatasetKeyOf(dataset))
		}
		rsp.Functions = append(rsp.Functions, state)
	}
	return rsp, nil
}
