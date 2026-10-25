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

// 分类、策略实例的私有因子集与参数提交（因子动态调参执行计划 2.2～2.4）。

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

func (s *Service) EnsureInstanceFactorSet(ctx context.Context, req *factorpb.EnsureInstanceFactorSetReq) (*factorpb.EnsureInstanceFactorSetRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.EnsureInstanceFactorSetRsp{RetInfo: unavailable(err)}, nil
	}
	if strings.TrimSpace(req.GetInstanceId()) == "" || strings.TrimSpace(req.GetSpaceId()) == "" {
		return &factorpb.EnsureInstanceFactorSetRsp{RetInfo: invalid(errors.New("instance_id and space_id are required"))}, nil
	}
	if space, scoped, err := callerSpace(ctx); err != nil || scoped && space != req.GetSpaceId() {
		if err == nil {
			err = fmt.Errorf("space %q does not match the request space %q", req.GetSpaceId(), space)
		}
		return &factorpb.EnsureInstanceFactorSetRsp{RetInfo: invalid(err)}, nil
	}
	members := make([]catalog.InstanceMember, 0, len(req.GetMembers()))
	for _, member := range req.GetMembers() {
		members = append(members, catalog.InstanceMember{Alias: member.GetMemberAlias(), FactorID: member.GetFactorId()})
	}
	result, err := s.catalog.EnsureInstanceSet(ctx, catalog.InstanceSetRequest{
		InstanceID: req.GetInstanceId(), SpaceID: req.GetSpaceId(), SourceDatasetID: req.GetSourceDatasetId(), Freq: req.GetFreq(),
		SubjectMode: req.GetSubjectMode(), Subjects: factorwire.CloneStrings(req.GetSubjects()), Members: members,
	})
	if err != nil {
		return &factorpb.EnsureInstanceFactorSetRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.EnsureInstanceFactorSetRsp{RetInfo: success(), FactorSet: factorwire.SetToPB(result.Set),
		Members: membersToPB(result.Members), ViewId: domain.ResultViewID(result.Set.SetID)}, nil
}

func (s *Service) SetInstanceFactorSetEnabled(ctx context.Context, req *factorpb.SetInstanceFactorSetEnabledReq) (*factorpb.SetInstanceFactorSetEnabledRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.SetInstanceFactorSetEnabledRsp{RetInfo: unavailable(err)}, nil
	}
	if strings.TrimSpace(req.GetInstanceId()) == "" {
		return &factorpb.SetInstanceFactorSetEnabledRsp{RetInfo: invalid(errors.New("instance_id is required"))}, nil
	}
	set, err := s.catalog.SetInstanceSetEnabled(ctx, req.GetInstanceId(), req.GetEnabled())
	if err != nil {
		return &factorpb.SetInstanceFactorSetEnabledRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.SetInstanceFactorSetEnabledRsp{RetInfo: success(), FactorSet: factorwire.SetToPB(set)}, nil
}

func (s *Service) ReleaseInstanceFactorSet(ctx context.Context, req *factorpb.ReleaseInstanceFactorSetReq) (*factorpb.ReleaseInstanceFactorSetRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.ReleaseInstanceFactorSetRsp{RetInfo: unavailable(err)}, nil
	}
	if strings.TrimSpace(req.GetInstanceId()) == "" {
		return &factorpb.ReleaseInstanceFactorSetRsp{RetInfo: invalid(errors.New("instance_id is required"))}, nil
	}
	if err := s.catalog.ReleaseInstanceSet(ctx, req.GetInstanceId()); err != nil {
		return &factorpb.ReleaseInstanceFactorSetRsp{RetInfo: inner(err)}, nil
	}
	return &factorpb.ReleaseInstanceFactorSetRsp{RetInfo: success()}, nil
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
	if strings.TrimSpace(req.GetWriter()) == "" || strings.TrimSpace(req.GetSetId()) == "" || strings.TrimSpace(req.GetRequestId()) == "" ||
		req.GetPeriodTime() <= 0 || len(req.GetMembers()) == 0 {
		return &factorpb.SubmitFactorParamsRsp{RetInfo: invalid(errors.New("writer, set_id, request_id, period_time and members are required"))}, nil
	}
	period := time.Unix(req.GetPeriodTime(), 0).UTC()
	var basedOn time.Time
	if req.GetBasedOnPeriod() > 0 {
		basedOn = time.Unix(req.GetBasedOnPeriod(), 0).UTC()
	}
	submissions := make([]catalog.ParamSubmission, 0, len(req.GetMembers()))
	for _, member := range req.GetMembers() {
		submissions = append(submissions, catalog.ParamSubmission{
			RequestID: req.GetRequestId(), SetID: req.GetSetId(), MemberAlias: strings.TrimSpace(member.GetMemberAlias()),
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
	if strings.TrimSpace(req.GetSetId()) == "" {
		return &factorpb.GetFactorParamsRsp{RetInfo: invalid(errors.New("set_id is required"))}, nil
	}
	if err := s.checkSetSpace(ctx, req.GetSetId()); err != nil {
		return &factorpb.GetFactorParamsRsp{RetInfo: invalid(err)}, nil
	}
	from, to := time.Unix(req.GetFrom(), 0).UTC(), time.Unix(req.GetTo(), 0).UTC()
	if req.GetTo() <= 0 {
		to = time.Now().UTC().Add(24 * time.Hour)
	}
	if to.Before(from) {
		return &factorpb.GetFactorParamsRsp{RetInfo: invalid(errors.New("to must not be before from"))}, nil
	}
	intervals, err := s.catalog.ListParamIntervals(ctx, req.GetSetId(), req.GetMemberAlias(), from, to)
	if err != nil {
		return &factorpb.GetFactorParamsRsp{RetInfo: inner(err)}, nil
	}
	rsp := &factorpb.GetFactorParamsRsp{RetInfo: success()}
	for _, interval := range intervals {
		rsp.Intervals = append(rsp.Intervals, factorwire.IntervalToPB(interval))
	}
	if req.GetIncludeRequests() {
		requests, err := s.catalog.ListParamRequests(ctx, req.GetSetId(), req.GetMemberAlias(), from, to, int(req.GetLimit()))
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
	if frozen, ok, err := s.catalog.FrozenThrough(ctx, req.GetSetId()); err != nil {
		return &factorpb.GetFactorParamsRsp{RetInfo: inner(err)}, nil
	} else if ok {
		rsp.FrozenThrough = frozen.Unix()
	}
	return rsp, nil
}
