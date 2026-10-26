package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
)

// 分类、策略实例的私有因子任务与参数提交（因子动态调参执行计划 2.2～2.4）。

func categoryToPB(category domain.FactorCategory) *factorpb.FactorCategory {
	return &factorpb.FactorCategory{CategoryId: category.CategoryID, Name: category.Name, SortOrder: int32(category.SortOrder),
		CreatedAt: factorwire.FormatTime(category.CreatedAt), UpdatedAt: factorwire.FormatTime(category.UpdatedAt)}
}

func (s *Service) ListFactorCategories(ctx context.Context, _ *factorpb.ListFactorCategoriesReq) (*factorpb.ListFactorCategoriesRsp, error) {
	categories, err := s.catalog.ListCategories(ctx)
	if err != nil {
		return &factorpb.ListFactorCategoriesRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*factorpb.FactorCategory, 0, len(categories))
	for _, category := range categories {
		out = append(out, categoryToPB(category))
	}
	return &factorpb.ListFactorCategoriesRsp{RetInfo: success(), Categories: out}, nil
}

func (s *Service) UpsertFactorCategory(ctx context.Context, req *factorpb.UpsertFactorCategoryReq) (*factorpb.UpsertFactorCategoryRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.UpsertFactorCategoryRsp{RetInfo: unavailable(err)}, nil
	}
	if req.GetCategory() == nil {
		return &factorpb.UpsertFactorCategoryRsp{RetInfo: invalid(errors.New("category is required"))}, nil
	}
	in := domain.FactorCategory{CategoryID: req.GetCategory().GetCategoryId(), Name: req.GetCategory().GetName(),
		SortOrder: int(req.GetCategory().GetSortOrder())}
	if err := domain.ValidateCategory(in); err != nil {
		return &factorpb.UpsertFactorCategoryRsp{RetInfo: invalid(err)}, nil
	}
	category, err := s.catalog.UpsertCategory(ctx, in)
	if err != nil {
		return &factorpb.UpsertFactorCategoryRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.UpsertFactorCategoryRsp{RetInfo: success(), Category: categoryToPB(category)}, nil
}

func (s *Service) DeleteFactorCategory(ctx context.Context, req *factorpb.DeleteFactorCategoryReq) (*factorpb.DeleteFactorCategoryRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.DeleteFactorCategoryRsp{RetInfo: unavailable(err)}, nil
	}
	if strings.TrimSpace(req.GetCategoryId()) == "" {
		return &factorpb.DeleteFactorCategoryRsp{RetInfo: invalid(errors.New("category_id is required"))}, nil
	}
	if err := s.catalog.DeleteCategory(ctx, req.GetCategoryId()); err != nil {
		return &factorpb.DeleteFactorCategoryRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.DeleteFactorCategoryRsp{RetInfo: success()}, nil
}

func (s *Service) SetFactorCategory(ctx context.Context, req *factorpb.SetFactorCategoryReq) (*factorpb.SetFactorCategoryRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.SetFactorCategoryRsp{RetInfo: unavailable(err)}, nil
	}
	if strings.TrimSpace(req.GetFactorId()) == "" {
		return &factorpb.SetFactorCategoryRsp{RetInfo: invalid(errors.New("factor_id is required"))}, nil
	}
	factor, err := s.catalog.SetFactorCategory(ctx, req.GetFactorId(), req.GetCategoryId())
	if err != nil {
		return &factorpb.SetFactorCategoryRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.SetFactorCategoryRsp{RetInfo: success(), Factor: factorwire.DefToPB(factor)}, nil
}

func (s *Service) EnsureInstanceFactorTask(ctx context.Context, req *factorpb.EnsureInstanceFactorTaskReq) (*factorpb.EnsureInstanceFactorTaskRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.EnsureInstanceFactorTaskRsp{RetInfo: unavailable(err)}, nil
	}
	if strings.TrimSpace(req.GetInstanceId()) == "" || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &factorpb.EnsureInstanceFactorTaskRsp{RetInfo: invalid(errors.New("instance_id and space_id are required"))}, nil
	}
	if space, scoped, err := callerSpace(ctx); err != nil || scoped && space != req.GetSpaceId() {
		if err == nil {
			err = fmt.Errorf("space %q does not match the request space %q", req.GetSpaceId(), space)
		}
		return &factorpb.EnsureInstanceFactorTaskRsp{RetInfo: invalid(err)}, nil
	}
	members := make([]catalog.InstanceMember, 0, len(req.GetMembers()))
	for _, member := range req.GetMembers() {
		members = append(members, catalog.InstanceMember{Alias: member.GetMemberAlias(), FactorID: member.GetFactorId()})
	}
	result, err := s.catalog.EnsureInstanceTask(ctx, catalog.InstanceTaskRequest{
		InstanceID: req.GetInstanceId(), SpaceID: req.GetSpaceId(), SourceDatasetID: req.GetSourceDatasetId(), Freq: req.GetFreq(),
		Members: members,
	})
	if err != nil {
		return &factorpb.EnsureInstanceFactorTaskRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.EnsureInstanceFactorTaskRsp{RetInfo: success(), FactorTask: factorwire.SetToPB(result.Set),
		Members: membersToPB(result.Members), ViewId: domain.ResultViewID(result.Set.TaskID)}, nil
}

func (s *Service) SetInstanceFactorTaskEnabled(ctx context.Context, req *factorpb.SetInstanceFactorTaskEnabledReq) (*factorpb.SetInstanceFactorTaskEnabledRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.SetInstanceFactorTaskEnabledRsp{RetInfo: unavailable(err)}, nil
	}
	if strings.TrimSpace(req.GetInstanceId()) == "" {
		return &factorpb.SetInstanceFactorTaskEnabledRsp{RetInfo: invalid(errors.New("instance_id is required"))}, nil
	}
	set, err := s.catalog.SetInstanceTaskEnabled(ctx, req.GetInstanceId(), req.GetEnabled())
	if err != nil {
		return &factorpb.SetInstanceFactorTaskEnabledRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.SetInstanceFactorTaskEnabledRsp{RetInfo: success(), FactorTask: factorwire.SetToPB(set)}, nil
}

func (s *Service) ReleaseInstanceFactorTask(ctx context.Context, req *factorpb.ReleaseInstanceFactorTaskReq) (*factorpb.ReleaseInstanceFactorTaskRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.ReleaseInstanceFactorTaskRsp{RetInfo: unavailable(err)}, nil
	}
	if strings.TrimSpace(req.GetInstanceId()) == "" {
		return &factorpb.ReleaseInstanceFactorTaskRsp{RetInfo: invalid(errors.New("instance_id is required"))}, nil
	}
	if err := s.catalog.ReleaseInstanceTask(ctx, req.GetInstanceId()); err != nil {
		return &factorpb.ReleaseInstanceFactorTaskRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.ReleaseInstanceFactorTaskRsp{RetInfo: success()}, nil
}

// SubmitFactorParams 只接受内部服务（参数写入方）的调用：带空间元数据的控制台调用直接拒绝，参数只能由写入方产出。
// 冻结与提交在同一个库里串行化，这里不受生命周期门禁影响以外的任何缓存。
func (s *Service) SubmitFactorParams(ctx context.Context, req *factorpb.SubmitFactorParamsReq) (*factorpb.SubmitFactorParamsRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.SubmitFactorParamsRsp{RetInfo: unavailable(err)}, nil
	}
	if _, scoped, err := callerSpace(ctx); err != nil || scoped {
		if err == nil {
			err = errors.New("参数只能由参数写入方（策略实例）提交")
		}
		return &factorpb.SubmitFactorParamsRsp{RetInfo: invalid(err)}, nil
	}
	if strings.TrimSpace(req.GetWriter()) == "" || strings.TrimSpace(req.GetTaskId()) == "" || strings.TrimSpace(req.GetRequestId()) == "" ||
		req.GetPeriodTime() <= 0 || len(req.GetMembers()) == 0 {
		return &factorpb.SubmitFactorParamsRsp{RetInfo: invalid(errors.New("writer, task_id, request_id, period_time and members are required"))}, nil
	}
	period := time.Unix(req.GetPeriodTime(), 0).UTC()
	var basedOn time.Time
	if req.GetBasedOnPeriod() > 0 {
		basedOn = time.Unix(req.GetBasedOnPeriod(), 0).UTC()
	}
	submissions := make([]catalog.ParamSubmission, 0, len(req.GetMembers()))
	for _, member := range req.GetMembers() {
		submissions = append(submissions, catalog.ParamSubmission{
			RequestID: req.GetRequestId(), TaskID: req.GetTaskId(), MemberAlias: strings.TrimSpace(member.GetMemberAlias()),
			PeriodTime: period, BasedOnPeriod: basedOn, Snapshot: factorwire.SnapshotFromPB(member.GetParams()),
		})
	}
	results, err := s.catalog.SubmitParams(ctx, req.GetWriter(), submissions)
	if err != nil {
		return &factorpb.SubmitFactorParamsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*factorpb.MemberSubmitResult, 0, len(results))
	for _, result := range results {
		out = append(out, &factorpb.MemberSubmitResult{MemberAlias: result.MemberAlias, Result: result.Result, Reason: result.Reason})
	}
	return &factorpb.SubmitFactorParamsRsp{RetInfo: success(), Results: out}, nil
}

func (s *Service) GetFactorParams(ctx context.Context, req *factorpb.GetFactorParamsReq) (*factorpb.GetFactorParamsRsp, error) {
	if strings.TrimSpace(req.GetTaskId()) == "" {
		return &factorpb.GetFactorParamsRsp{RetInfo: invalid(errors.New("task_id is required"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.GetFactorParamsRsp{RetInfo: invalid(err)}, nil
	}
	from, to := time.Unix(req.GetFrom(), 0).UTC(), time.Unix(req.GetTo(), 0).UTC()
	if req.GetTo() <= 0 {
		to = time.Now().UTC().Add(24 * time.Hour)
	}
	if to.Before(from) {
		return &factorpb.GetFactorParamsRsp{RetInfo: invalid(errors.New("to must not be before from"))}, nil
	}
	intervals, err := s.catalog.ListParamIntervals(ctx, req.GetTaskId(), req.GetMemberAlias(), from, to)
	if err != nil {
		return &factorpb.GetFactorParamsRsp{RetInfo: inner(err)}, nil
	}
	rsp := &factorpb.GetFactorParamsRsp{RetInfo: success()}
	for _, interval := range intervals {
		rsp.Intervals = append(rsp.Intervals, factorwire.IntervalToPB(interval))
	}
	if req.GetIncludeRequests() {
		requests, err := s.catalog.ListParamRequests(ctx, req.GetTaskId(), req.GetMemberAlias(), from, to, int(req.GetLimit()))
		if err != nil {
			return &factorpb.GetFactorParamsRsp{RetInfo: inner(err)}, nil
		}
		for _, request := range requests {
			info := &factorpb.ParamRequestInfo{Seq: request.Seq, RequestId: request.RequestID, MemberAlias: request.MemberAlias,
				PeriodTime: request.PeriodTime.Unix(), Params: factorwire.SnapshotToPB(request.Snapshot), Status: request.Status,
				Writer: request.Writer, CreatedAt: factorwire.FormatTime(request.CreatedAt)}
			if !request.BasedOnPeriod.IsZero() {
				info.BasedOnPeriod = request.BasedOnPeriod.Unix()
			}
			rsp.Requests = append(rsp.Requests, info)
		}
	}
	if frozen, ok, err := s.catalog.FrozenThrough(ctx, req.GetTaskId()); err != nil {
		return &factorpb.GetFactorParamsRsp{RetInfo: inner(err)}, nil
	} else if ok {
		rsp.FrozenThrough = frozen.Unix()
	}
	return rsp, nil
}
