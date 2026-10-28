package rpc

import (
	"os"
	"testing"

	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/stretchr/testify/require"
)

// 模板的保存、列表、渲染与删除；渲染结果能直接用于 CreateStrategy。
func TestStrategyTemplateLifecycle(t *testing.T) {
	h := newHarness(t)
	raw, err := os.ReadFile("../../templates/tranche_example.yaml")
	require.NoError(t, err)
	saved, err := h.service.SaveStrategyTemplate(h.ctx, &strategypb.SaveStrategyTemplateReq{TemplateYaml: string(raw)})
	require.NoError(t, err)
	requireOK(t, int32(saved.GetRetInfo().GetCode()), saved.GetRetInfo().GetMsg())
	require.Equal(t, "tranche_example", saved.GetTemplate().GetTemplateId())
	require.Equal(t, []string{"h", "n", "m", "p", "w"}, saved.GetTemplate().GetColumns())
	require.Equal(t, []string{"total", "leverage", "max_missing"}, saved.GetTemplate().GetVariables())

	bad, _ := h.service.SaveStrategyTemplate(h.ctx, &strategypb.SaveStrategyTemplateReq{TemplateYaml: "template_id: x\nname: X\ndsl: |\n  name: \"{{nope}}\"\n"})
	require.NotZero(t, bad.GetRetInfo().GetCode())

	list, _ := h.service.ListStrategyTemplates(h.ctx, &strategypb.ListStrategyTemplatesReq{})
	requireOK(t, int32(list.GetRetInfo().GetCode()), list.GetRetInfo().GetMsg())
	require.Len(t, list.GetTemplates(), 1)

	rendered, _ := h.service.RenderStrategyTemplate(h.ctx, &strategypb.RenderStrategyTemplateReq{
		TemplateId: "tranche_example", Variables: map[string]string{"name": "demo"}, ParamsCsv: "h,n,m,p,w\n1,20,24,0.1,0.5\n",
	})
	requireOK(t, int32(rendered.GetRetInfo().GetCode()), rendered.GetRetInfo().GetMsg())
	created, _ := h.service.CreateStrategy(h.ctx, &strategypb.CreateStrategyReq{Strategy: &strategypb.Strategy{DslYaml: rendered.GetDslYaml()}})
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	require.Equal(t, "demo", created.GetStrategy().GetName())

	empty, _ := h.service.RenderStrategyTemplate(h.ctx, &strategypb.RenderStrategyTemplateReq{TemplateId: "tranche_example", Variables: map[string]string{"name": "x"}})
	require.Contains(t, empty.GetRetInfo().GetMsg(), "参数表不能为空")

	deleted, _ := h.service.DeleteStrategyTemplate(h.ctx, &strategypb.DeleteStrategyTemplateReq{TemplateId: "tranche_example"})
	requireOK(t, int32(deleted.GetRetInfo().GetCode()), deleted.GetRetInfo().GetMsg())
	missing, _ := h.service.GetStrategyTemplate(h.ctx, &strategypb.GetStrategyTemplateReq{TemplateId: "tranche_example"})
	require.Contains(t, missing.GetRetInfo().GetMsg(), "不存在")
}
