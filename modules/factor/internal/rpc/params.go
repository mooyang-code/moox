package rpc

import (
	"context"
	"errors"
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

// SubmitFactorParams 只接受内部服务（参数写入方）的调用：带空间元数据的控制台调用直接拒绝，参数只能由关联的策略实例提交。
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
	submissions := make([]catalog.MemberSubmission, 0, len(req.GetMembers()))
	for _, member := range req.GetMembers() {
		submissions = append(submissions, catalog.MemberSubmission{
			MemberKey: strings.TrimSpace(member.GetMemberKey()), Snapshot: factorwire.SnapshotFromPB(member.GetParams()),
		})
	}
	results, err := s.catalog.SubmitParams(ctx, req.GetWriter(), req.GetTaskId(), time.Unix(req.GetPeriodTime(), 0).UTC(), req.GetRequestId(), submissions)
	if err != nil {
		return &factorpb.SubmitFactorParamsRsp{RetInfo: inner(err)}, nil
	}
	out := make([]*factorpb.MemberSubmitResult, 0, len(results))
	for _, result := range results {
		out = append(out, &factorpb.MemberSubmitResult{MemberKey: result.MemberKey, Result: result.Result, Reason: result.Reason})
	}
	return &factorpb.SubmitFactorParamsRsp{RetInfo: success(), Results: out}, nil
}

// GetFactorParams 只读：返回任务里各可变成员最近一次被接受的提交，策略与控制台都能读。
func (s *Service) GetFactorParams(ctx context.Context, req *factorpb.GetFactorParamsReq) (*factorpb.GetFactorParamsRsp, error) {
	if strings.TrimSpace(req.GetTaskId()) == "" {
		return &factorpb.GetFactorParamsRsp{RetInfo: invalid(errors.New("task_id is required"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.GetFactorParamsRsp{RetInfo: invalid(err)}, nil
	}
	members, err := s.catalog.GetParams(ctx, req.GetTaskId())
	if err != nil {
		return &factorpb.GetFactorParamsRsp{RetInfo: inner(err)}, nil
	}
	rsp := &factorpb.GetFactorParamsRsp{RetInfo: success()}
	for _, member := range members {
		rsp.Members = append(rsp.Members, factorwire.MemberParamStateToPB(member))
	}
	return rsp, nil
}
