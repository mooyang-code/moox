package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/internal/tagattr"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/rs/xid"
)

// resampleSource 是由源采集任务推导出的合成源参数。
type resampleSource struct {
	TaskID       string
	DatasetID    string
	Frequency    string
	SeriesTag    string
	Provider     string
	MarketType   string
	SubjectCount int
	Dataset      storagesource.DatasetInfo
}

// resampleSourceOf 由源采集任务（kline 或 kline_resample）推导合成的源参数：源数据集是源任务的结果数据集，
// 源周期是它采集（或合成）的周期，序列标签优先取源数据集属性与已登记序列快照，尚未登记快照时再退回采集路由推导。
func (s *Service) resampleSourceOf(ctx context.Context, spaceID, sourceTaskID string) (resampleSource, error) {
	sourceTaskID = strings.TrimSpace(sourceTaskID)
	if sourceTaskID == "" {
		return resampleSource{}, errors.New("source_task_id is required")
	}
	task, err := s.taskRepo.GetByTaskID(ctx, spaceID, sourceTaskID)
	if err != nil || task == nil {
		return resampleSource{}, fmt.Errorf("source task %q not found", sourceTaskID)
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.DataType)
	if err != nil {
		return resampleSource{}, fmt.Errorf("source task %q has invalid params: %w", sourceTaskID, err)
	}
	ids := taskresult.PersistedResultIDs(task.TaskID, task.ResultDatasetID)
	out := resampleSource{TaskID: task.TaskID, DatasetID: ids.DatasetID}
	if s.datasetSrc != nil {
		info, infoErr := s.datasetSrc.GetDataset(ctx, task.SpaceID, out.DatasetID)
		if infoErr != nil {
			return resampleSource{}, fmt.Errorf("source task %q result Dataset is not ready: %w", sourceTaskID, infoErr)
		}
		out.Dataset = info
		out.Provider = strings.TrimSpace(info.DataSourceID)
		if out.Provider == "" {
			out.Provider = strings.TrimSpace(info.Attributes["source_data_source_id"])
		}
		out.MarketType = strings.ToLower(strings.TrimSpace(info.Attributes["market_type"]))
		subjects, subjectsErr := s.datasetSrc.ResolveSubjects(ctx, task.SpaceID, info.SubjectTags)
		if subjectsErr == nil {
			out.SubjectCount = len(subjects)
		}
	}
	switch strings.ToLower(task.DataType) {
	case "kline_resample":
		out.Frequency = params.TargetFrequency
		out.SeriesTag = firstNonEmptyString(
			params.SourceSeriesTag,
			out.Dataset.Attributes["source_series_tag"],
			out.Dataset.Attributes["series_tag"],
		)
	case "kline":
		out.Frequency = params.Frequency
		seriesTag, provider, marketType, tagErr := s.resolveKlineSourceSeriesTag(ctx, *task, params, out.Dataset)
		if tagErr != nil {
			return resampleSource{}, tagErr
		}
		out.SeriesTag = seriesTag
		if out.Provider == "" {
			out.Provider = provider
		}
		if out.MarketType == "" {
			out.MarketType = marketType
		}
	default:
		return resampleSource{}, fmt.Errorf("source task %q is %s, not a K-line task", sourceTaskID, task.DataType)
	}
	if strings.TrimSpace(out.Frequency) == "" {
		return resampleSource{}, fmt.Errorf("source task %q has no frequency", sourceTaskID)
	}
	if strings.TrimSpace(out.SeriesTag) == "" {
		return resampleSource{}, fmt.Errorf("source task %q has no series tag", sourceTaskID)
	}
	return out, nil
}

// resolveKlineSourceSeriesTag 解析 K 线源任务的序列标签：
// 1. 优先读源数据集属性（source_series_tag / series_tag）；
// 2. 其次读已登记标的时间线快照（t_collector_series_timeline）最新生效段的唯一序列标签；
// 3. 尚未登记快照（如刚创建源任务尚未首次对账）时，再退回按源任务标签推导。
func (s *Service) resolveKlineSourceSeriesTag(
	ctx context.Context,
	task domain.CollectionTask,
	params *domain.CollectParams,
	info storagesource.DatasetInfo,
) (seriesTag, provider, marketType string, err error) {
	if attrTag := firstNonEmptyString(info.Attributes["source_series_tag"], info.Attributes["series_tag"]); attrTag != "" {
		return attrTag, strings.TrimSpace(info.DataSourceID), strings.ToLower(strings.TrimSpace(info.Attributes["market_type"])), nil
	}
	ids := taskresult.PersistedResultIDs(task.TaskID, task.ResultDatasetID)
	if timelineTag, timelineErr := s.seriesTagFromTimeline(ctx, task.SpaceID, ids.DatasetID, params.Frequency); timelineErr != nil {
		return "", "", "", fmt.Errorf("源任务 %q 的序列标签：%w", task.TaskID, timelineErr)
	} else if timelineTag != "" {
		return timelineTag, strings.TrimSpace(info.DataSourceID), strings.ToLower(strings.TrimSpace(info.Attributes["market_type"])), nil
	}
	tagSource, ok := s.datasetSrc.(collectionTaskTagSource)
	if !ok {
		return "", "", "", errors.New("source tag lookup is unavailable")
	}
	tagIDs := normalizeTagIDs(task.TagIDs)
	if len(tagIDs) == 0 {
		return "", "", "", fmt.Errorf("source task %q has no tags", task.TaskID)
	}
	for _, tagID := range tagIDs {
		tag, tagErr := tagSource.GetTag(ctx, task.SpaceID, tagID)
		if tagErr != nil || tag == nil {
			return "", "", "", fmt.Errorf("load source tag %s: %v", tagID, tagErr)
		}
		p, m := tagattr.Source(tag), tagattr.MarketType(tag)
		candidate, tagErr := fetchplan.TaskSeriesTag(task.SpaceID, p, m, s.sourceIDResolver)
		if tagErr != nil {
			return "", "", "", fmt.Errorf("源任务 %q 的序列标签：%w", task.TaskID, tagErr)
		}
		if seriesTag != "" && seriesTag != candidate {
			return "", "", "", fmt.Errorf("源任务 %q 的标签写入不同的序列（%s、%s），合成只支持单一序列", task.TaskID, seriesTag, candidate)
		}
		seriesTag, provider, marketType = candidate, p, m
	}
	return seriesTag, provider, marketType, nil
}

// seriesTagFromTimeline 从已登记标的时间线取该（数据集, 频率）最新生效段的唯一序列标签；未登记或已清空时返回空串。
func (s *Service) seriesTagFromTimeline(ctx context.Context, spaceID, datasetID, frequency string) (string, error) {
	if s == nil || s.persistence == nil {
		return "", nil
	}
	entries, err := s.persistence.SeriesTimeline().ListBySpace(ctx, spaceID)
	if err != nil {
		return "", nil
	}
	// 按写入先后重放同一生效点（后写覆盖先写），取生效点最晚的一段。
	byEffective := map[int64]store.SeriesTimelineEntry{}
	for _, entry := range entries {
		if entry.DatasetID == datasetID && strings.EqualFold(entry.Frequency, frequency) {
			byEffective[entry.EffectiveFrom] = entry
		}
	}
	var latestEffective int64
	var latest store.SeriesTimelineEntry
	found := false
	for effective, entry := range byEffective {
		if !found || effective > latestEffective {
			latestEffective, latest, found = effective, entry, true
		}
	}
	if !found || len(latest.Series) == 0 {
		return "", nil
	}
	seen := map[string]struct{}{}
	tags := make([]string, 0, 2)
	for _, item := range latest.Series {
		tag := strings.TrimSpace(item.SeriesTag)
		if tag == "" {
			continue
		}
		if _, exists := seen[tag]; !exists {
			seen[tag] = struct{}{}
			tags = append(tags, tag)
		}
	}
	if len(tags) == 0 {
		return "", nil
	}
	if len(tags) > 1 {
		sort.Strings(tags)
		return "", fmt.Errorf("数据集包含多条序列标签（%s），合成只支持单一序列", strings.Join(tags, "、"))
	}
	return tags[0], nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// deriveResampleSource 创建合成任务时用 source_task_id 推导并写入 source_dataset_id、source_frequency、source_series_tag 等源参数。
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
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	task.CollectParams = string(encoded)
	return nil
}

// PreviewKlineResampleSource 由源采集任务推导合成的源参数、分桶时钟与实际可合成列，并按合成规则校验目标频率。
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
	market, err := barclock.MarketForDataset(source.Dataset.Alignment, source.Dataset.Attributes["calendar"])
	if err != nil {
		rsp.ValidationError = err.Error()
		return rsp, nil
	}
	rsp.Alignment = market.Alignment
	rsp.CalendarId = market.CalendarID
	rsp.AllowedFrequencies = s.allowedResampleFrequencies(market, source.Frequency)
	rules, colErr := collectorresample.AvailableRulesForSource(source.Dataset.ColumnTypes)
	if colErr != nil {
		rsp.ValidationError = colErr.Error()
		return rsp, nil
	}
	rsp.AllowedOutputFields = outputFieldOptionsForRules(rules)
	if target := strings.TrimSpace(req.GetTargetFrequency()); target != "" {
		preview := map[string]any{
			"source_dataset_id": source.DatasetID, "source_frequency": source.Frequency,
			"target_frequency": target, "source_series_tag": source.SeriesTag,
		}
		raw, _ := json.Marshal(preview)
		params, parseErr := domain.ParseCollectParams(string(raw), "kline_resample")
		if parseErr == nil {
			parseErr = collectorresample.ValidateTaskParamsForSource(source.Dataset, params, "dataset_preview")
		}
		if parseErr != nil {
			rsp.ValidationError = parseErr.Error()
		}
	}
	return rsp, nil
}

func outputFieldOptionsForRules(rules []collectorresample.FieldRule) []*pb.OutputFieldOption {
	options := make([]*pb.OutputFieldOption, 0, len(rules))
	for _, rule := range rules {
		displayName := rule.Name
		group := taskresult.GroupTrading
		valueType := taskresult.ValueTypeName(rule.ValueType)
		if spec, ok := taskresult.KlineOutputField(rule.Name); ok {
			displayName = spec.DisplayName
			group = spec.Group
			valueType = taskresult.ValueTypeName(spec.Kind)
		}
		options = append(options, &pb.OutputFieldOption{
			FieldId:     rule.Name,
			DisplayName: displayName,
			Group:       group,
			ValueType:   valueType,
		})
	}
	return options
}

// allowedResampleFrequencies 返回数据集时钟允许、且能由源周期合成的目标周期：比源周期长、是它的整数倍，加上 1d。
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
		NotBefore: unixMillis(job.NotBefore), Status: job.Status, TotalBuckets: uint32(job.TotalBuckets), DoneBuckets: uint32(job.DoneBuckets),
		Written: uint64(job.Written), Skipped: uint64(job.Skipped), SkippedSamples: job.SkippedSamples, LastError: job.LastError,
		CreatedAt: unixMillis(job.CreatedAt), UpdatedAt: unixMillis(job.UpdatedAt),
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
	params, err := domain.ParseCollectParams(collection.CollectParams, collection.DataType)
	if err != nil {
		return fail(pb.ErrorCode_INVALID_PARAM, err.Error())
	}
	var source storagesource.DatasetInfo
	if s.datasetSrc != nil {
		source, err = s.datasetSrc.GetDataset(ctx, req.GetSpaceId(), params.SourceDatasetID)
		if err != nil {
			return fail(pb.ErrorCode_NOT_FOUND, err.Error())
		}
		if err := checkSourceRetention(source, start, time.Now().UTC()); err != nil {
			return fail(pb.ErrorCode_INVALID_PARAM, err.Error())
		}
	}
	task, err := collectorresample.NewTaskForSource(*collection, source)
	if err != nil {
		return fail(pb.ErrorCode_INVALID_PARAM, err.Error())
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
