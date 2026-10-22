package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/rs/xid"
)

// defaultSeriesTag 是没有登记序列（stockcn 的行情序列固定为 default）时合成源使用的序列标签。
const stockSeriesTag = "default"

// resampleSource 是由源采集任务推导出的合成源参数。
type resampleSource struct {
	TaskID       string
	DatasetID    string
	Frequency    string
	SeriesTag    string
	Provider     string
	MarketType   string
	SubjectCount int
}

// resampleSourceOf 由源采集任务（kline 或 kline_resample）推导合成的源参数：源数据集是源任务的结果数据集，
// 源周期是它采集（或合成）的周期，序列标签与采集写入时用的相同。
func (s *Service) resampleSourceOf(ctx context.Context, spaceID, sourceTaskID string) (resampleSource, error) {
	sourceTaskID = strings.TrimSpace(sourceTaskID)
	if sourceTaskID == "" {
		return resampleSource{}, errors.New("source_task_id is required")
	}
	task, err := s.taskRepo.GetByTaskID(ctx, spaceID, sourceTaskID)
	if err != nil || task == nil {
		return resampleSource{}, fmt.Errorf("source task %q not found", sourceTaskID)
	}
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	if err != nil {
		return resampleSource{}, fmt.Errorf("source task %q has invalid params: %w", sourceTaskID, err)
	}
	ids := taskresult.PersistedResultIDs(task.SpaceID, task.TaskID, task.ResultDatasetID, task.ResultViewID)
	out := resampleSource{TaskID: task.TaskID, DatasetID: ids.DatasetID}
	switch strings.ToLower(task.DataType) {
	case "kline_resample":
		out.Frequency, out.SeriesTag, out.Provider, out.MarketType = params.TargetFrequency, params.SourceSeriesTag, params.Provider, params.MarketType
	case "kline":
		out.Frequency = params.Frequency
		series, listErr := s.taskRepo.ListTaskSeries(ctx, task.SpaceID, task.TaskID)
		if listErr != nil {
			return resampleSource{}, listErr
		}
		tags := map[string]struct{}{}
		for _, item := range series {
			tags[item.SeriesTag] = struct{}{}
			if out.Provider == "" {
				out.Provider, out.MarketType = item.Provider, item.MarketType
			}
		}
		switch {
		case len(tags) == 1:
			for tag := range tags {
				out.SeriesTag = tag
			}
		case len(tags) > 1:
			return resampleSource{}, fmt.Errorf("source task %q writes %d series tags; a resample needs exactly one", sourceTaskID, len(tags))
		case strings.EqualFold(task.SpaceID, "stockcn"):
			out.SeriesTag = stockSeriesTag
		default:
			return resampleSource{}, fmt.Errorf("source task %q has no series yet; wait for its collection plan", sourceTaskID)
		}
	default:
		return resampleSource{}, fmt.Errorf("source task %q is %s, not a K-line task", sourceTaskID, task.DataType)
	}
	if strings.TrimSpace(out.Frequency) == "" {
		return resampleSource{}, fmt.Errorf("source task %q has no frequency", sourceTaskID)
	}
	if s.datasetSrc != nil {
		info, infoErr := s.datasetSrc.GetDataset(ctx, task.SpaceID, out.DatasetID)
		if infoErr != nil {
			return resampleSource{}, fmt.Errorf("source task %q result Dataset is not ready: %w", sourceTaskID, infoErr)
		}
		subjects, subjectsErr := s.datasetSrc.ResolveSubjects(ctx, task.SpaceID, info.SubjectTags)
		if subjectsErr == nil {
			out.SubjectCount = len(subjects)
		}
	}
	return out, nil
}

// deriveResampleSource 创建合成任务时用 source_task_id 推导并写入 source_dataset_id、source_frequency、source_series_tag 等源参数，
// 前端只提交源任务与目标频率，手填的序列标签不会再出错。没有 source_task_id（配置文件导入）时参数保持原样。
func (s *Service) deriveResampleSource(ctx context.Context, task *domain.CollectionTask) error {
	if !strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
		return nil
	}
	values := map[string]any{}
	if err := json.Unmarshal([]byte(task.CollectParams), &values); err != nil {
		return nil // 参数格式错误由后面的校验报告
	}
	sourceTaskID, _ := values["source_task_id"].(string)
	if strings.TrimSpace(sourceTaskID) == "" {
		return nil
	}
	if strings.TrimSpace(sourceTaskID) == strings.TrimSpace(task.TaskID) {
		return errors.New("source_task_id must not be the task itself")
	}
	source, err := s.resampleSourceOf(ctx, task.SpaceID, sourceTaskID)
	if err != nil {
		return err
	}
	values["source_task_id"] = source.TaskID
	values["source_dataset_id"] = source.DatasetID
	values["source_frequency"] = source.Frequency
	values["source_series_tag"] = source.SeriesTag
	if source.Provider != "" {
		values["provider"] = source.Provider
	}
	if source.MarketType != "" {
		values["market_type"] = source.MarketType
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	task.CollectParams = string(encoded)
	return nil
}

// PreviewKlineResampleSource 由源采集任务推导合成的源参数，并按合成规则校验目标频率。
func (s *Service) PreviewKlineResampleSource(ctx context.Context, req *pb.PreviewKlineResampleSourceReq) (*pb.PreviewKlineResampleSourceRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetSourceTaskId()) == "" {
		return &pb.PreviewKlineResampleSourceRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id and source_task_id are required")}, nil
	}
	source, err := s.resampleSourceOf(ctx, req.GetSpaceId(), req.GetSourceTaskId())
	if err != nil {
		return &pb.PreviewKlineResampleSourceRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	rsp := &pb.PreviewKlineResampleSourceRsp{
		RetInfo: retOK(), SourceDatasetId: source.DatasetID, SourceFrequency: source.Frequency, SourceSeriesTag: source.SeriesTag,
		SubjectCount: uint32(source.SubjectCount), MarketId: req.GetSpaceId(), Provider: source.Provider, MarketType: source.MarketType,
	}
	market, err := barclock.MarketFor(req.GetSpaceId())
	if err != nil {
		rsp.ValidationError = err.Error()
		return rsp, nil
	}
	rsp.Alignment = market.Alignment
	rsp.AllowedFrequencies = s.allowedResampleFrequencies(market, source.Frequency)
	if target := strings.TrimSpace(req.GetTargetFrequency()); target != "" {
		raw, _ := json.Marshal(map[string]any{
			"provider": source.Provider, "market_type": source.MarketType, "source_dataset_id": source.DatasetID, "source_frequency": source.Frequency,
			"source_series_tag": source.SeriesTag, "target_dataset_id": "dataset_preview", "target_frequency": target,
		})
		params, parseErr := domain.ParseCollectParams(string(raw), "", "", "kline_resample")
		if parseErr == nil {
			parseErr = collectorresample.ValidateTaskParams(req.GetSpaceId(), params)
		}
		if parseErr != nil {
			rsp.ValidationError = parseErr.Error()
		}
	}
	return rsp, nil
}

// allowedResampleFrequencies 返回市场允许、且能由源周期合成的目标周期：比源周期长、是它的整数倍，加上 1d。
func (s *Service) allowedResampleFrequencies(market barclock.Market, sourceFrequency string) []string {
	source, err := collectorresample.ParseFixedFrequency(sourceFrequency)
	if err != nil {
		return nil
	}
	var out []string
	for _, candidate := range append(market.AllowedIntraday(), "1d") {
		target, parseErr := collectorresample.ParseFixedFrequency(candidate)
		if parseErr != nil || collectorresample.ValidateResamplePair(source, target) != nil {
			continue
		}
		if clock, clockErr := market.Clock(target.Duration); clockErr != nil || clock.ValidateSource(source.Duration) != nil {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func resampleJobToPB(job store.ResampleJob) *pb.ResampleJob {
	return &pb.ResampleJob{
		JobId: job.JobID, TaskId: job.TaskID, RequestId: job.RequestID, Origin: job.Origin, Subjects: job.Subjects,
		Start: job.Start.Format(time.RFC3339), End: job.End.Format(time.RFC3339), NextBucket: job.NextBucket.Format(time.RFC3339),
		NotBefore: job.NotBefore.Format(time.RFC3339), Status: job.Status, TotalBuckets: uint32(job.TotalBuckets), DoneBuckets: uint32(job.DoneBuckets),
		Written: uint64(job.Written), Skipped: uint64(job.Skipped), SkippedSamples: job.SkippedSamples, LastError: job.LastError,
		CreatedAt: job.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: job.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// SubmitKlineResampleJob 提交历史合成作业：范围按 UTC 解析，向外对齐到目标桶，最多 10080 个桶；同一 request_id 重复提交返回已有作业。
func (s *Service) SubmitKlineResampleJob(ctx context.Context, req *pb.SubmitKlineResampleJobReq) (*pb.SubmitKlineResampleJobRsp, error) {
	fail := func(code pb.ErrorCode, msg string) (*pb.SubmitKlineResampleJobRsp, error) {
		return &pb.SubmitKlineResampleJobRsp{RetInfo: retErr(code, msg)}, nil
	}
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetTaskId()) == "" || strings.TrimSpace(req.GetRequestId()) == "" {
		return fail(pb.ErrorCode_INVALID_PARAM, "space_id, task_id and request_id are required")
	}
	start, err := parseUTCTime(req.GetStart())
	if err != nil {
		return fail(pb.ErrorCode_INVALID_PARAM, "start must be RFC3339 or YYYY-MM-DD[ HH:MM[:SS]] in UTC")
	}
	end, err := parseUTCTime(req.GetEnd())
	if err != nil {
		return fail(pb.ErrorCode_INVALID_PARAM, "end must be RFC3339 or YYYY-MM-DD[ HH:MM[:SS]] in UTC")
	}
	collection, err := s.taskRepo.GetByTaskID(ctx, req.GetSpaceId(), req.GetTaskId())
	if err != nil || collection == nil {
		return fail(pb.ErrorCode_NOT_FOUND, "task not found")
	}
	if !strings.EqualFold(collection.DataType, "kline_resample") {
		return fail(pb.ErrorCode_INVALID_PARAM, "task is not kline_resample")
	}
	if !collection.Enabled {
		return fail(pb.ErrorCode_INVALID_PARAM, "task is disabled")
	}
	if collection.PrepareState != domain.PrepareStateReady {
		return fail(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("task is not ready (prepare_state=%s)", collection.PrepareState))
	}
	task, err := collectorresample.NewTask(*collection)
	if err != nil {
		return fail(pb.ErrorCode_INVALID_PARAM, err.Error())
	}
	if s.datasetSrc != nil {
		source, sourceErr := s.datasetSrc.GetDataset(ctx, req.GetSpaceId(), task.Spec.SourceDatasetID)
		if sourceErr != nil {
			return fail(pb.ErrorCode_NOT_FOUND, sourceErr.Error())
		}
		if err := checkSourceRetention(source, start, time.Now().UTC()); err != nil {
			return fail(pb.ErrorCode_INVALID_PARAM, err.Error())
		}
	}
	job, created, err := collectorresample.SubmitJob(ctx, s.persistence.Resample(), task, collectorresample.SubmitJobRequest{
		SpaceID: req.GetSpaceId(), TaskID: req.GetTaskId(), RequestID: strings.TrimSpace(req.GetRequestId()),
		Origin: store.ResampleOriginManual, Subjects: req.GetSubjects(), Start: start, End: end,
	}, func() string { return xid.New().String() }, time.Now().UTC())
	if err != nil {
		return fail(pb.ErrorCode_INVALID_PARAM, err.Error())
	}
	return &pb.SubmitKlineResampleJobRsp{RetInfo: retOK(), Job: resampleJobToPB(job), Created: created}, nil
}

// checkSourceRetention 拒绝早于源数据集保留期的范围：那段源数据已被清理，合成不出来。
func checkSourceRetention(source storagesource.DatasetInfo, start, now time.Time) error {
	if window, bounded := source.RetentionWindow(); bounded && start.Before(now.Add(-window)) {
		return fmt.Errorf("start is older than source Dataset retention %s", source.Retention)
	}
	return nil
}

// parseUTCTime 按 UTC 解析时间；无时区的输入视为 UTC。
func parseUTCTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q", raw)
}

// GetKlineResampleJob 读取一个历史合成作业。
func (s *Service) GetKlineResampleJob(ctx context.Context, req *pb.GetKlineResampleJobReq) (*pb.GetKlineResampleJobRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetJobId()) == "" {
		return &pb.GetKlineResampleJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id and job_id are required")}, nil
	}
	job, err := s.persistence.Resample().GetJob(ctx, req.GetSpaceId(), req.GetJobId())
	if errors.Is(err, store.ErrResampleJobNotFound) {
		return &pb.GetKlineResampleJobRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "job not found")}, nil
	}
	if err != nil {
		return &pb.GetKlineResampleJobRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.GetKlineResampleJobRsp{RetInfo: retOK(), Job: resampleJobToPB(job)}, nil
}

// ListKlineResampleJobs 按任务、状态列出历史合成作业，最新的在前。
func (s *Service) ListKlineResampleJobs(ctx context.Context, req *pb.ListKlineResampleJobsReq) (*pb.ListKlineResampleJobsRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.ListKlineResampleJobsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	jobs, err := s.persistence.Resample().ListJobs(ctx, req.GetSpaceId(), strings.TrimSpace(req.GetTaskId()), strings.TrimSpace(req.GetStatus()), int(req.GetLimit()))
	if err != nil {
		return &pb.ListKlineResampleJobsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.ListKlineResampleJobsRsp{RetInfo: retOK()}
	for _, job := range jobs {
		rsp.Jobs = append(rsp.Jobs, resampleJobToPB(job))
	}
	return rsp, nil
}

// CancelKlineResampleJob 取消排队或运行中的作业（已写入的保留）。
func (s *Service) CancelKlineResampleJob(ctx context.Context, req *pb.CancelKlineResampleJobReq) (*pb.CancelKlineResampleJobRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetJobId()) == "" {
		return &pb.CancelKlineResampleJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id and job_id are required")}, nil
	}
	job, err := s.persistence.Resample().CancelJob(ctx, req.GetSpaceId(), req.GetJobId())
	switch {
	case errors.Is(err, store.ErrResampleJobNotFound):
		return &pb.CancelKlineResampleJobRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "job not found")}, nil
	case errors.Is(err, store.ErrResampleJobFinished):
		return &pb.CancelKlineResampleJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job is already finished"), Job: resampleJobToPB(job)}, nil
	case err != nil:
		return &pb.CancelKlineResampleJobRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.CancelKlineResampleJobRsp{RetInfo: retOK(), Job: resampleJobToPB(job)}, nil
}
