package template

import (
	"os"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

func loadBuiltin(t *testing.T) *Template {
	t.Helper()
	raw, err := os.ReadFile("../../templates/tranche_example.yaml")
	require.NoError(t, err)
	tpl, err := Parse(raw)
	require.NoError(t, err)
	return tpl
}

// 内置示例模板：变量按类型写入，参数表行按字面量类型填入，生成的 DSL 通过校验并展开成子策略。
func TestRenderBuiltinExample(t *testing.T) {
	tpl := loadBuiltin(t)
	require.Equal(t, []string{"h", "n", "m", "p", "w"}, tpl.Columns())
	rows, err := tpl.ParseRows("h,n,m,p,w\n1,20,24,0.1,0.5\n3,60,12,0.2,0.5\n")
	require.NoError(t, err)
	text, err := tpl.Render(map[string]string{"name": "demo", "leverage": "3"}, rows)
	require.NoError(t, err)
	strategy, err := dsl.Parse([]byte(text))
	require.NoError(t, err)
	require.Equal(t, "demo", strategy.Name)
	require.Equal(t, "3", strategy.Portfolio.Leverage.String())
	subs := strategy.Rules[0].Tranche.Subs
	require.Len(t, subs, 2)
	require.Equal(t, 3, subs[1].HoldPeriod)
	require.Equal(t, "momentum__60", subs[1].LongFactors[0].Column)
	require.Equal(t, 0.5, subs[1].StgWeight)
}

func TestRenderRejects(t *testing.T) {
	tpl := loadBuiltin(t)
	rows := [][]string{{"1", "20", "24", "0.1", "0.5"}}
	cases := map[string]struct {
		vars map[string]string
		rows [][]string
		want string
	}{
		"缺策略名":       {map[string]string{}, rows, "缺少变量 name"},
		"变量越界":       {map[string]string{"name": "a", "leverage": "11"}, rows, "大于上限"},
		"类型不符":       {map[string]string{"name": "a", "total": "x"}, rows, "不是有限的数字"},
		"未声明变量":      {map[string]string{"name": "a", "foo": "1"}, rows, "没有变量 foo"},
		"花括号":        {map[string]string{"name": "{h}"}, rows, "花括号"},
		"内嵌花括号":      {map[string]string{"name": "foo{h}"}, rows, "花括号"},
		"空参数表":       {map[string]string{"name": "a"}, nil, "参数表不能为空"},
		"列数不符":       {map[string]string{"name": "a"}, [][]string{{"1"}}, "有 1 列"},
		"单元格含花括号":    {map[string]string{"name": "a"}, [][]string{{"1", "20", "24", "0.1", "{w}"}}, "花括号"},
		"生成的 DSL 无效": {map[string]string{"name": "a"}, [][]string{{"0", "20", "24", "0.1", "0.5"}}, "hold_period"},
	}
	for name, c := range cases {
		_, err := tpl.Render(c.vars, c.rows)
		require.ErrorContains(t, err, c.want, name)
	}
	_, err := tpl.ParseRows("h,s\n1,2\n")
	require.ErrorContains(t, err, "表头")
}

func TestParseRejectsBadSkeleton(t *testing.T) {
	base := "template_id: t\nname: T\nvariables:\n  - {name: n, type: int, default: \"5\"}\ndsl: |\n"
	cases := map[string]string{
		"键上占位符":  "  name: \"{{name}}\"\n  \"{{n}}\": 1\n",
		"部分占位符":  "  name: \"x-{{name}}\"\n",
		"未声明变量":  "  name: \"{{nope}}\"\n",
		"参数表行非空": "  name: \"{{name}}\"\n  rules:\n    - id: g\n      type: tranche\n      weight: 1\n      tranche: {hold_period: \"{h}\", long_factors: [{factor: x}]}\n      params: {columns: [h], rows: [[1]]}\n",
	}
	for name, skeleton := range cases {
		_, err := Parse([]byte(base + indent(skeleton)))
		require.Error(t, err, name)
	}
	// 无参数表的 rank 模板：只用变量，不接受参数表，也不能写 sample_row。
	rank := base + indent("  name: \"{{name}}\"\n  rules:\n    - id: r\n      type: rank\n      score: \"m\"\n      select: {top: \"{{n}}\"}\n      weight: {total: 1}\n")
	tpl, err := Parse([]byte(rank))
	require.NoError(t, err)
	text, err := tpl.Render(map[string]string{"name": "R", "n": "3"}, nil)
	require.NoError(t, err)
	require.Contains(t, text, "top: 3")
	_, err = tpl.Render(map[string]string{"name": "R"}, [][]string{{"1"}})
	require.ErrorContains(t, err, "不接受参数表")
}

func indent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "  " + line
	}
	return strings.Join(lines, "\n") + "\n"
}
