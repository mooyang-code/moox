package rpc

import (
	"context"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/modules/factor/internal/preview"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/factorexpr"
)

// Previewer 在因子任务的源数据上试算一个定义。
type Previewer interface {
	Preview(ctx context.Context, req preview.Request) (preview.Result, error)
}

// 校验与试算时临时定义使用的 factor_id 与 name：只用于编译，不落库。
const draftFactorID, draftFactorName = "draft", "draft"

// draftDefinition 把请求里的 spec 编译成临时定义。
func draftDefinition(spec string, allowPartial bool) (domain.FactorDef, error) {
	return domain.NormalizeFactorDefinition(domain.FactorDef{
		FactorID: draftFactorID, Name: draftFactorName, Spec: spec, AllowPartialUniverse: allowPartial,
	})
}

// ValidateFactorSpec 编译一个定义并返回诊断与派生元数据；定义不合法时 valid=false 且 ret_info 仍为成功。
func (s *Service) ValidateFactorSpec(_ context.Context, req *factorpb.ValidateFactorSpecReq) (*factorpb.ValidateFactorSpecRsp, error) {
	if req == nil {
		return &factorpb.ValidateFactorSpecRsp{RetInfo: invalid(errors.New("spec is required"))}, nil
	}
	def, err := draftDefinition(req.GetSpec(), req.GetAllowPartialUniverse())
	if err != nil {
		return &factorpb.ValidateFactorSpecRsp{RetInfo: success(), Valid: false, Diagnostic: diagnosticToPB(err)}, nil
	}
	compiled := def.Compiled()
	return &factorpb.ValidateFactorSpecRsp{
		RetInfo: success(), Valid: true, FactorType: def.FactorType,
		InputColumns: factorwire.CloneStrings(def.InputColumns), Outputs: factorwire.CloneStrings(def.Outputs),
		LookbackPeriods: int32(def.LookbackPeriods), Canonical: compiled.CanonicalText(), DefinitionHash: domain.DefinitionHash(def),
		Params: factorwire.DefToPB(def).GetParams(),
	}, nil
}

// diagnosticToPB 取出编译错误里的位置信息；不是编译错误时只有消息。
func diagnosticToPB(err error) *factorpb.FactorSpecDiagnostic {
	var exprErr *factorexpr.Error
	if errors.As(err, &exprErr) {
		return &factorpb.FactorSpecDiagnostic{
			Path: exprErr.Path, DocLine: int32(exprErr.DocLine), Line: int32(exprErr.Line), Column: int32(exprErr.Column),
			Message: exprErr.Message,
		}
	}
	return &factorpb.FactorSpecDiagnostic{Message: err.Error()}
}

// PreviewFactor 在指定因子任务的源数据上试算一个定义。
func (s *Service) PreviewFactor(ctx context.Context, req *factorpb.PreviewFactorReq) (*factorpb.PreviewFactorRsp, error) {
	if req == nil || req.GetTaskId() == "" {
		return &factorpb.PreviewFactorRsp{RetInfo: invalid(errors.New("task_id is required"))}, nil
	}
	if s.previewer == nil {
		return &factorpb.PreviewFactorRsp{RetInfo: inner(errors.New("试算未启用"))}, nil
	}
	if err := s.checkTaskSpace(ctx, req.GetTaskId()); err != nil {
		return &factorpb.PreviewFactorRsp{RetInfo: invalid(err)}, nil
	}
	def, err := draftDefinition(req.GetSpec(), req.GetAllowPartialUniverse())
	if err != nil {
		return &factorpb.PreviewFactorRsp{RetInfo: invalid(fmt.Errorf("定义不合法：%w", err))}, nil
	}
	set, _, err := s.catalog.GetTask(ctx, req.GetTaskId())
	if err != nil {
		return &factorpb.PreviewFactorRsp{RetInfo: invalid(err)}, nil
	}
	result, err := s.previewer.Preview(ctx, preview.Request{
		Set: set, Factor: def, Subjects: factorwire.CloneStrings(req.GetSubjects()), Periods: int(req.GetPeriods()),
		Params: factorwire.SnapshotFromPB(req.GetParams()),
	})
	if err != nil {
		return &factorpb.PreviewFactorRsp{RetInfo: invalid(err)}, nil
	}
	rows := make([]*factorpb.PreviewRow, 0, len(result.Rows))
	for _, row := range result.Rows {
		out := &factorpb.PreviewRow{
			SubjectId: row.Subject, SeriesTag: row.Tag, DataTime: factorwire.FormatTime(row.DataTime),
			Values: make([]float64, len(row.Values)), Nulls: make([]bool, len(row.Values)),
		}
		for i, value := range row.Values {
			if value == nil {
				out.Nulls[i] = true
			} else {
				out.Values[i] = *value
			}
		}
		rows = append(rows, out)
	}
	return &factorpb.PreviewFactorRsp{RetInfo: success(), Columns: result.Columns, Rows: rows}, nil
}

// ListFactorFunctions 返回表达式可用的函数及其说明，供控制台的编辑器提示与文档使用。
func (s *Service) ListFactorFunctions(context.Context, *factorpb.ListFactorFunctionsReq) (*factorpb.ListFactorFunctionsRsp, error) {
	docs := factorexpr.FunctionDocs()
	functions := make([]*factorpb.FactorFunction, 0, len(docs))
	for _, doc := range docs {
		args := make([]*factorpb.FactorFunctionArg, 0, len(doc.Args))
		for _, arg := range doc.Args {
			args = append(args, &factorpb.FactorFunctionArg{
				Name: arg.Name, Kind: arg.Kind, Min: arg.Min, Max: arg.Max, Optional: arg.Optional, DefaultValue: arg.Default,
			})
		}
		functions = append(functions, &factorpb.FactorFunction{
			Name: doc.Name, Category: doc.Category, Signature: doc.Signature, Result: doc.Result, Args: args,
			Lookback: doc.Lookback, Doc: doc.Doc, Example: doc.Example,
		})
	}
	return &factorpb.ListFactorFunctionsRsp{RetInfo: success(), Functions: functions, ExprVersion: factorexpr.ExprVersion}, nil
}
