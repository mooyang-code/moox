package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"gorm.io/gorm"
)

// UdfAPI 是自定义算子的目录边界（由 catalog.Service 实现）。
type UdfAPI interface {
	PublishUdf(ctx context.Context, raw []byte) (*catalog.PublishResult, error)
	ActivateUdf(ctx context.Context, name, hash string, dryRun bool) (*catalog.ActivateResult, error)
	ListUdfs(ctx context.Context) ([]catalog.UdfInfo, error)
	GetUdf(ctx context.Context, name string) (catalog.UdfInfo, error)
	UdfModule(ctx context.Context, hash string) (string, []byte, error)
}

// udfError 把自定义算子相关的错误映射成返回码：包不合格是参数错误，冲突（前提不满足）是冲突，不存在是参数错误。
func udfError(err error) *commonpb.RetInfo {
	switch {
	case errors.Is(err, catalog.ErrInvalidUdf):
		return invalid(err)
	case errors.Is(err, store.ErrConflict):
		return &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: err.Error()}
	case errors.Is(err, gorm.ErrRecordNotFound):
		return invalid(fmt.Errorf("自定义算子或版本不存在"))
	}
	return inner(err)
}

func (s *Service) udf() UdfAPI {
	if api, ok := s.catalog.(UdfAPI); ok {
		return api
	}
	return nil
}

// PublishUdf 上传 .udfpkg：复核后存为不可变版本（未启用）。
func (s *Service) PublishUdf(ctx context.Context, req *factorpb.PublishUdfReq) (*factorpb.PublishUdfRsp, error) {
	if err := s.mutationAllowed(); err != nil {
		return &factorpb.PublishUdfRsp{RetInfo: unavailable(err)}, nil
	}
	api := s.udf()
	if api == nil {
		return &factorpb.PublishUdfRsp{RetInfo: inner(errors.New("自定义算子未启用"))}, nil
	}
	if len(req.GetPackage()) == 0 {
		return &factorpb.PublishUdfRsp{RetInfo: invalid(errors.New("package 必填"))}, nil
	}
	res, err := api.PublishUdf(ctx, req.GetPackage())
	if err != nil {
		return &factorpb.PublishUdfRsp{RetInfo: udfError(err)}, nil
	}
	report, _ := marshalReport(res)
	return &factorpb.PublishUdfRsp{RetInfo: success(), Name: res.Name, Hash: res.Hash, Created: res.Created, Report: report}, nil
}

func marshalReport(res *catalog.PublishResult) (string, error) {
	if res.Report == nil {
		return "", nil
	}
	raw, err := json.Marshal(res.Report)
	return string(raw), err
}

// ListUdfs 返回全部自定义算子与引擎上报的各版本模块状态。
func (s *Service) ListUdfs(ctx context.Context, _ *factorpb.ListUdfsReq) (*factorpb.ListUdfsRsp, error) {
	api := s.udf()
	if api == nil {
		return &factorpb.ListUdfsRsp{RetInfo: inner(errors.New("自定义算子未启用"))}, nil
	}
	list, err := api.ListUdfs(ctx)
	if err != nil {
		return &factorpb.ListUdfsRsp{RetInfo: udfError(err)}, nil
	}
	modules := s.engineModules(ctx)
	rsp := &factorpb.ListUdfsRsp{RetInfo: success()}
	for _, info := range list {
		rsp.Udfs = append(rsp.Udfs, udfInfoToPB(info, modules))
	}
	return rsp, nil
}

// GetUdf 返回一个自定义算子。
func (s *Service) GetUdf(ctx context.Context, req *factorpb.GetUdfReq) (*factorpb.GetUdfRsp, error) {
	api := s.udf()
	if api == nil {
		return &factorpb.GetUdfRsp{RetInfo: inner(errors.New("自定义算子未启用"))}, nil
	}
	if strings.TrimSpace(req.GetName()) == "" {
		return &factorpb.GetUdfRsp{RetInfo: invalid(errors.New("name 必填"))}, nil
	}
	info, err := api.GetUdf(ctx, req.GetName())
	if err != nil {
		return &factorpb.GetUdfRsp{RetInfo: udfError(err)}, nil
	}
	return &factorpb.GetUdfRsp{RetInfo: success(), Udf: udfInfoToPB(info, s.engineModules(ctx))}, nil
}

// engineModules 返回引擎最近一次心跳里的模块状态（没有引擎或没有心跳时为空）。
func (s *Service) engineModules(ctx context.Context) []domain.UDFModuleStatus {
	if s.engine == nil {
		return nil
	}
	_, status, seen, err := s.engine.Engine(ctx)
	if err != nil || !seen {
		return nil
	}
	return status.UDFModules
}

func udfInfoToPB(info catalog.UdfInfo, modules []domain.UDFModuleStatus) *factorpb.UdfInfo {
	out := &factorpb.UdfInfo{
		Name: info.Name.Name, ActiveHash: info.Name.ActiveHash,
		CreatedAt: factorwire.FormatTime(info.Name.CreatedAt), UpdatedAt: factorwire.FormatTime(info.Name.UpdatedAt),
		ReferencedBy: factorwire.CloneStrings(info.Referenced),
	}
	own := map[string]bool{}
	for _, v := range info.Versions {
		own[v.Hash] = true
		out.Versions = append(out.Versions, &factorpb.UdfVersionInfo{
			Hash: v.Hash, Name: v.Name, Manifest: v.Manifest, SourceRef: v.SourceRef, SdkVersion: v.SDKVersion, Toolchain: v.Toolchain,
			Report: v.Report, CreatedAt: factorwire.FormatTime(v.CreatedAt), Active: v.Hash == info.Name.ActiveHash,
		})
	}
	for _, m := range modules {
		if own[m.Hash] {
			out.Modules = append(out.Modules, &factorpb.UdfModuleStatus{Hash: m.Hash, Name: m.Name, State: m.State, Error: m.Error})
		}
	}
	return out
}

// ActivateUdf 切换启用版本；dry_run 只返回影响清单与前提检查。前提不满足时返回 CONFLICT，同时带上完整的检查结果。
func (s *Service) ActivateUdf(ctx context.Context, req *factorpb.ActivateUdfReq) (*factorpb.ActivateUdfRsp, error) {
	if !req.GetDryRun() {
		if err := s.mutationAllowed(); err != nil {
			return &factorpb.ActivateUdfRsp{RetInfo: unavailable(err)}, nil
		}
	}
	api := s.udf()
	if api == nil {
		return &factorpb.ActivateUdfRsp{RetInfo: inner(errors.New("自定义算子未启用"))}, nil
	}
	name, hash := strings.TrimSpace(req.GetName()), strings.TrimSpace(req.GetHash())
	if name == "" || hash == "" {
		return &factorpb.ActivateUdfRsp{RetInfo: invalid(errors.New("name 与 hash 必填"))}, nil
	}
	res, err := api.ActivateUdf(ctx, name, hash, req.GetDryRun())
	rsp := &factorpb.ActivateUdfRsp{RetInfo: success()}
	if res != nil {
		rsp = activateToPB(res)
	}
	if err != nil {
		rsp.RetInfo = udfError(err)
	}
	return rsp, nil
}

func activateToPB(res *catalog.ActivateResult) *factorpb.ActivateUdfRsp {
	rsp := &factorpb.ActivateUdfRsp{
		RetInfo: success(), Name: res.Name, OldHash: res.OldHash, NewHash: res.NewHash, DryRun: res.DryRun, Applied: res.Applied,
		Tasks: factorwire.CloneStrings(res.Tasks), Instances: factorwire.CloneStrings(res.Instances), Notes: factorwire.CloneStrings(res.Notes),
	}
	for _, impact := range res.Impacts {
		rsp.Impacts = append(rsp.Impacts, &factorpb.UdfImpact{
			FactorId: impact.FactorID, OldDefinitionHash: impact.OldDefinitionHash, NewDefinitionHash: impact.NewDefinitionHash,
			OldLookback: int32(impact.OldLookback), NewLookback: int32(impact.NewLookback), InputColumns: factorwire.CloneStrings(impact.InputColumns),
			Members: factorwire.CloneStrings(impact.Members), Error: impact.Error,
		})
	}
	for _, p := range res.Prerequisites {
		rsp.Prerequisites = append(rsp.Prerequisites, &factorpb.UdfPrerequisite{Name: p.Name, Ok: p.OK, Detail: p.Detail})
	}
	return rsp
}
