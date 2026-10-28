package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/template"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// 策略模板（设计第 6 节）：模板只用于生成策略 DSL，生成的策略走现有的 CreateStrategy，之后与模板脱钩。

func templateProto(value store.Template) *strategypb.StrategyTemplate {
	out := &strategypb.StrategyTemplate{TemplateId: value.TemplateID, Name: value.Name, Description: value.Description, TemplateYaml: value.TemplateYAML,
		CreatedAt: formatTime(value.CreatedAt), UpdatedAt: formatTime(value.UpdatedAt)}
	// 已存的模板都经过校验；这里解析只为给出参数列与变量名，失败时（例如校验规则收紧）照常返回原文。
	if parsed, err := template.Parse([]byte(value.TemplateYAML)); err == nil {
		out.Columns = parsed.Columns()
		for _, variable := range parsed.Variables {
			out.Variables = append(out.Variables, variable.Name)
		}
	}
	return out
}

func (s *Service) ListStrategyTemplates(ctx context.Context, _ *strategypb.ListStrategyTemplatesReq) (*strategypb.ListStrategyTemplatesRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.ListStrategyTemplatesRsp{RetInfo: failure(err)}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.ListStrategyTemplatesRsp{RetInfo: invalid(err)}, nil
	}
	templates, err := s.Store.ListTemplates(ctx)
	if err != nil {
		return &strategypb.ListStrategyTemplatesRsp{RetInfo: failure(err)}, nil
	}
	out := make([]*strategypb.StrategyTemplate, 0, len(templates))
	for _, value := range templates {
		out = append(out, templateProto(value))
	}
	return &strategypb.ListStrategyTemplatesRsp{RetInfo: success(), Templates: out}, nil
}

func (s *Service) loadTemplate(ctx context.Context, templateID string) (store.Template, error) {
	if _, err := requireSpaceID(ctx); err != nil {
		return store.Template{}, err
	}
	if strings.TrimSpace(templateID) == "" {
		return store.Template{}, errors.New("template_id 不能为空")
	}
	value, found, err := s.Store.GetTemplate(ctx, templateID)
	if err != nil {
		return store.Template{}, err
	}
	if !found {
		return store.Template{}, fmt.Errorf("模板 %s 不存在", templateID)
	}
	return value, nil
}

func (s *Service) GetStrategyTemplate(ctx context.Context, req *strategypb.GetStrategyTemplateReq) (*strategypb.GetStrategyTemplateRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.GetStrategyTemplateRsp{RetInfo: failure(err)}, nil
	}
	value, err := s.loadTemplate(ctx, req.GetTemplateId())
	if err != nil {
		return &strategypb.GetStrategyTemplateRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.GetStrategyTemplateRsp{RetInfo: success(), Template: templateProto(value)}, nil
}

// SaveStrategyTemplate 按模板 ID 新建或整体替换；保存前用默认值与 sample_row 试渲染并校验生成的 DSL。
func (s *Service) SaveStrategyTemplate(ctx context.Context, req *strategypb.SaveStrategyTemplateReq) (*strategypb.SaveStrategyTemplateRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.SaveStrategyTemplateRsp{RetInfo: failure(err)}, nil
	}
	if _, err := requireSpaceID(ctx); err != nil {
		return &strategypb.SaveStrategyTemplateRsp{RetInfo: invalid(err)}, nil
	}
	parsed, err := template.Parse([]byte(req.GetTemplateYaml()))
	if err != nil {
		return &strategypb.SaveStrategyTemplateRsp{RetInfo: invalid(err)}, nil
	}
	saved, err := s.Store.SaveTemplate(ctx, store.Template{TemplateID: parsed.ID, Name: parsed.Name, Description: parsed.Description, TemplateYAML: req.GetTemplateYaml(), UpdatedAt: s.nowTime()})
	if err != nil {
		return &strategypb.SaveStrategyTemplateRsp{RetInfo: failure(err)}, nil
	}
	return &strategypb.SaveStrategyTemplateRsp{RetInfo: success(), Template: templateProto(saved)}, nil
}

func (s *Service) DeleteStrategyTemplate(ctx context.Context, req *strategypb.DeleteStrategyTemplateReq) (*strategypb.DeleteStrategyTemplateRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.DeleteStrategyTemplateRsp{RetInfo: failure(err)}, nil
	}
	if _, err := s.loadTemplate(ctx, req.GetTemplateId()); err != nil {
		return &strategypb.DeleteStrategyTemplateRsp{RetInfo: invalid(err)}, nil
	}
	if _, err := s.Store.DeleteTemplate(ctx, req.GetTemplateId()); err != nil {
		return &strategypb.DeleteStrategyTemplateRsp{RetInfo: failure(err)}, nil
	}
	return &strategypb.DeleteStrategyTemplateRsp{RetInfo: success()}, nil
}

// RenderStrategyTemplate 用变量与参数表生成策略 DSL 并做完整校验，不落库；建策略时把结果交给 CreateStrategy。
func (s *Service) RenderStrategyTemplate(ctx context.Context, req *strategypb.RenderStrategyTemplateReq) (*strategypb.RenderStrategyTemplateRsp, error) {
	if err := s.ready(); err != nil {
		return &strategypb.RenderStrategyTemplateRsp{RetInfo: failure(err)}, nil
	}
	value, err := s.loadTemplate(ctx, req.GetTemplateId())
	if err != nil {
		return &strategypb.RenderStrategyTemplateRsp{RetInfo: invalid(err)}, nil
	}
	parsed, err := template.Parse([]byte(value.TemplateYAML))
	if err != nil {
		return &strategypb.RenderStrategyTemplateRsp{RetInfo: invalid(fmt.Errorf("模板 %s 已不能通过校验：%w", value.TemplateID, err))}, nil
	}
	rows, err := parsed.ParseRows(req.GetParamsCsv())
	if err != nil {
		return &strategypb.RenderStrategyTemplateRsp{RetInfo: invalid(err)}, nil
	}
	text, err := parsed.Render(req.GetVariables(), rows)
	if err != nil {
		return &strategypb.RenderStrategyTemplateRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.RenderStrategyTemplateRsp{RetInfo: success(), DslYaml: text}, nil
}
