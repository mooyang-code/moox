package factorexpr

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testUDFs() UDFs {
	return UDFs{
		"kalman_trend": {
			Name: "kalman_trend", Hash: strings.Repeat("a", 64), Inputs: []string{"x"},
			Params:    []UDFParam{{Name: "n", Int: true, Min: 2, Max: 500}, {Name: "q", Min: 0, Max: 1}},
			LookbackA: 4, LookbackParam: "n", Doc: "卡尔曼趋势", Example: "udf.kalman_trend(close, 20, 0.1)",
		},
		"pair": {
			Name: "pair", Hash: strings.Repeat("b", 64), Inputs: []string{"x", "y"},
			Params: []UDFParam{{Name: "n", Int: true, Min: 2, Max: 50}}, LookbackA: 1, LookbackParam: "n", Relaxed: true, MinValid: 2,
		},
		"fixed": {Name: "fixed", Hash: strings.Repeat("c", 64), Inputs: []string{"x"}, LookbackB: 7},
		"off":   {Name: "off"},
	}
}

func compileUDF(t *testing.T, env UDFEnv, spec string) (*Factor, error) {
	t.Helper()
	return CompileWith(spec, env)
}

func TestUDFCallSyntaxAndTypes(t *testing.T) {
	env := testUDFs()
	ok := []string{
		"output: udf.kalman_trend(close, 20, 0.1)",
		"output: cs_rank(udf.kalman_trend(ts_mean(close, 5) / ts_std(close, 5), 20, 0.1))",
		"output: udf.pair(close, volume, 10)",
		"output: udf.fixed(close)",
		"output: fill_nan(udf.fixed(close), 0)",
	}
	for _, spec := range ok {
		f, err := compileUDF(t, env, spec)
		require.NoError(t, err, spec)
		require.NotEmpty(t, f.UDFs, spec)
	}
	bad := map[string]string{
		"close.x(1)":                         "只能调用函数名",
		"udf.x.y(close)":                     "只能调用函数名",
		"foo.x(close)":                       "只能调用函数名",
		"udf.kalman_trend":                   "成员访问",
		"udf.nope(close)":                    "未知的自定义算子 udf.nope；可用：",
		"udf.off(close)":                     "没有启用的版本",
		"udf.kalman_trend(close, 20)":        "需要 3 个参数",
		"udf.kalman_trend(close, 1, 0.1)":    "必须是 2～500",
		"udf.kalman_trend(close, 20, 2)":     "必须是 0～1",
		"udf.kalman_trend(close, 20.5, 0.1)": "整数常量",
		"udf.kalman_trend(3, 20, 0.1)":       "必须依赖输入列",
		"udf.pair(close, 10)":                "需要 3 个参数",
	}
	for expr, want := range bad {
		_, err := compileUDF(t, env, "output: "+expr)
		require.Error(t, err, expr)
		require.Contains(t, err.Error(), want, expr)
	}
	// 没有解析环境时 udf. 调用报错，内置算子不受影响。
	_, err := Compile("output: udf.fixed(close)")
	require.ErrorContains(t, err, "没有可用的自定义算子")
	_, err = Compile("output: ts_mean(close, 5)")
	require.NoError(t, err)
}

func TestUDFLookbackStacksOnInputLookback(t *testing.T) {
	env := testUDFs()
	// udf 回看 L = 4*n = 80（n=20），L−1 = 79；输入 ts_mean(close, 5) 回看 5：总计 5 + 79 = 84。
	f, err := compileUDF(t, env, "output: udf.kalman_trend(ts_mean(close, 5), 20, 0.1)")
	require.NoError(t, err)
	require.Equal(t, 5+4*20-1, f.Lookback)

	// 参数槽：回看取声明范围的上限（n 最大 = 500 → L = 2000）。
	spec := "params:\n  n: {type: int, min: 2, max: 500, default: 20}\noutput: udf.kalman_trend(close, n, 0.1)\n"
	f, err = compileUDF(t, env, spec)
	require.NoError(t, err)
	require.Equal(t, 1+(4*500-1), f.Lookback, "最坏情况按参数端点")
	root := f.Outputs[0].Root
	require.True(t, root.HasParamSlots())
	require.Equal(t, 4*20-1, root.Function.Back(root.Ints), "默认值下的回看")
	require.Equal(t, 0, root.Function.Shift(root.Ints))

	f, err = compileUDF(t, env, "output: udf.fixed(close)")
	require.NoError(t, err)
	require.Equal(t, 1+6, f.Lookback)

	// 回看超过上限被拒绝。
	huge := UDFs{"big": {Name: "big", Hash: strings.Repeat("d", 64), Inputs: []string{"x"}, LookbackB: int64(MaxLookback) + 10}}
	_, err = compileUDF(t, huge, "output: udf.big(close)")
	require.ErrorContains(t, err, "超过上限")
}

func TestUDFIdentityFlowsIntoCanonicalTextAndKeys(t *testing.T) {
	env := testUDFs()
	a, err := compileUDF(t, env, "output: udf.kalman_trend(close, 20, 0.1)")
	require.NoError(t, err)
	text := a.CanonicalText()
	require.Contains(t, text, "udf.kalman_trend@"+strings.Repeat("a", 64)+"(close,20,0.1)")

	// 同一份定义在另一个版本下：规范文本不同。
	other := testUDFs()
	def := other["kalman_trend"]
	def.Hash = strings.Repeat("e", 64)
	other["kalman_trend"] = def
	b, err := compileUDF(t, other, "output: udf.kalman_trend(close, 20, 0.1)")
	require.NoError(t, err)
	require.NotEqual(t, text, b.CanonicalText())

	// 编译期去重键（programKey）随哈希区分：同名不同版本的两个成员不会合并。
	program, err := NewProgram([]ProgramInput{{Key: "a", FactorID: "fa", Factor: a}, {Key: "b", FactorID: "fb", Factor: b}})
	require.NoError(t, err)
	var udfNodes int
	for _, node := range program.Nodes {
		if node.Function.UDF() != nil {
			udfNodes++
		}
	}
	require.Equal(t, 2, udfNodes)

	// 同一版本、同参数的两个成员合并成一个节点。
	c, err := compileUDF(t, env, "output: udf.kalman_trend(close, 20, 0.1)")
	require.NoError(t, err)
	program, err = NewProgram([]ProgramInput{{Key: "a", FactorID: "fa", Factor: a}, {Key: "c", FactorID: "fc", Factor: c}})
	require.NoError(t, err)
	udfNodes = 0
	for _, node := range program.Nodes {
		if node.Function.UDF() != nil {
			udfNodes++
		}
	}
	require.Equal(t, 1, udfNodes)
}

func TestFactorRecordsUDFReferences(t *testing.T) {
	env := testUDFs()
	f, err := compileUDF(t, env, "let:\n  a: udf.pair(close, volume, 10)\noutput: udf.kalman_trend(a, 20, 0.1) + udf.fixed(close)\n")
	require.NoError(t, err)
	require.Equal(t, []UDFRef{
		{Name: "fixed", Hash: strings.Repeat("c", 64)},
		{Name: "kalman_trend", Hash: strings.Repeat("a", 64)},
		{Name: "pair", Hash: strings.Repeat("b", 64)},
	}, f.UDFs)
	plain, err := Compile("output: ts_mean(close, 5)")
	require.NoError(t, err)
	require.Empty(t, plain.UDFs)
}

func TestFunctionDocsListUDFs(t *testing.T) {
	docs := FunctionDocsWith(testUDFs())
	var found *FunctionDoc
	for i := range docs {
		if docs[i].Name == "udf.kalman_trend" {
			found = &docs[i]
		}
		require.NotEqual(t, "udf.off", docs[i].Name, "没有启用版本的不列出")
	}
	require.NotNil(t, found)
	require.Equal(t, "timeseries", found.Category)
	require.Equal(t, "udf.kalman_trend(x, n, q)", found.Signature)
	require.Equal(t, "卡尔曼趋势", found.Doc)
	require.Equal(t, len(FunctionDocs())+3, len(docs))
}
