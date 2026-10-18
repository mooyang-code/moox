package dsl

import (
	"reflect"
	"strings"
	"testing"
)

func columnSet(columns ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		set[column] = struct{}{}
	}
	return set
}

func TestAnalyzeExtractsNormalizersAndColumns(t *testing.T) {
	t.Parallel()
	expression, err := Analyze("0.6 * rank(bias_q_20) + 0.4 * rank(quote_volume_mean_q_20) - close", StageScore)
	if err != nil {
		t.Fatalf("Analyze() 出错：%v", err)
	}
	if len(expression.Normalizers) != 2 || expression.Normalizers[0].Kind != "rank" || expression.Normalizers[0].Inner.Source != "bias_q_20" || expression.Normalizers[1].Inner.Source != "quote_volume_mean_q_20" {
		t.Fatalf("截面函数不符：%+v", expression.Normalizers)
	}
	if !strings.Contains(expression.Rewritten, "__norm_0") || !strings.Contains(expression.Rewritten, "__norm_1") || strings.Contains(expression.Rewritten, "rank(") {
		t.Fatalf("改写后的表达式不符：%q", expression.Rewritten)
	}
	if !reflect.DeepEqual(expression.Columns, []string{"close"}) {
		t.Fatalf("直接引用列不符：%v", expression.Columns)
	}
	if !reflect.DeepEqual(expression.AllColumns(), []string{"bias_q_20", "close", "quote_volume_mean_q_20"}) {
		t.Fatalf("全部引用列不符：%v", expression.AllColumns())
	}
}

func TestAnalyzeNestedNormalizers(t *testing.T) {
	t.Parallel()
	expression, err := Analyze("rank(zscore(close * 2) + volume)", StageScore)
	if err != nil {
		t.Fatalf("Analyze() 出错：%v", err)
	}
	if len(expression.Normalizers) != 1 || expression.Normalizers[0].Kind != "rank" {
		t.Fatalf("外层截面函数不符：%+v", expression.Normalizers)
	}
	inner := expression.Normalizers[0].Inner
	if len(inner.Normalizers) != 1 || inner.Normalizers[0].Kind != "zscore" || inner.Normalizers[0].Inner.Source != "close * 2" {
		t.Fatalf("内层截面函数不符：%+v", inner.Normalizers)
	}
	if !reflect.DeepEqual(expression.AllColumns(), []string{"close", "volume"}) {
		t.Fatalf("全部引用列不符：%v", expression.AllColumns())
	}
}

func TestAnalyzeBarsAccess(t *testing.T) {
	t.Parallel()
	expression, err := Analyze("bars[-1].close <= bars[-1].ma_20 && bars[0].close > bars[0].ma_20", StageEntry)
	if err != nil {
		t.Fatalf("Analyze() 出错：%v", err)
	}
	if !reflect.DeepEqual(expression.Columns, []string{"close", "ma_20"}) || !reflect.DeepEqual(expression.PreviousColumns, []string{"close", "ma_20"}) {
		t.Fatalf("当期列 = %v，上一根列 = %v", expression.Columns, expression.PreviousColumns)
	}
	if strings.Contains(expression.Rewritten, "bars[-1]") || !strings.Contains(expression.Rewritten, "bars[1]") {
		t.Fatalf("改写后的表达式不符：%q", expression.Rewritten)
	}
}

func TestAnalyzeRejectsForbiddenConstructs(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		source string
		stage  Stage
		want   string
	}{
		"内置函数":          {"len(close) > 0", StageFilter, "内置函数"},
		"未知函数":          {"sqrt(close) > 0", StageFilter, "不允许调用函数"},
		"rank 在 filter": {"rank(close) > 0.5", StageFilter, "只能在 score"},
		"rank 多参数":      {"rank(close, volume)", StageScore, "一个参数"},
		"score 在 score": {"score + 1", StageScore, "score 只能"},
		"bars 下标 2":     {"bars[2].close > 0", StageFilter, "0 或 -1"},
		"bars 单独使用":     {"bars > 0", StageFilter, "bars 必须"},
		"bars 无列名":      {"bars[0] > 0", StageFilter, "bars"},
		"属性访问":          {"close.value > 0", StageFilter, "属性访问"},
		"保留名":           {"__norm_0 > 0", StageFilter, "保留名称"},
		"数组":            {"close in [1, 2]", StageFilter, "数组"},
		"语法错误":          {"close >", StageFilter, "语法错误"},
		"空表达式":          {"   ", StageFilter, "为空"},
		"谓词":            {"all(bars, {true})", StageFilter, "内置函数"},
	}
	for name, tc := range cases {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Analyze(tc.source, tc.stage)
			if err == nil {
				t.Fatalf("Analyze(%q) 应拒绝", tc.source)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误 = %v，应包含 %q", err, tc.want)
			}
		})
	}
}

func TestCompileExpressionRunsAgainstRowEnvironment(t *testing.T) {
	t.Parallel()
	columns := columnSet("close", "ma_20", "bias_q_20")
	score, err := CompileExpression("0.6 * rank(bias_q_20) + 0.4 * close", StageScore, columns)
	if err != nil {
		t.Fatalf("CompileExpression() 出错：%v", err)
	}
	value, err := score.Run(map[string]any{"close": 10.0, "ma_20": 0.0, "bias_q_20": 0.0, "bars": []map[string]float64{{}, {}}, "score": 0.0, "instrument_id": "BTC-USDT", "__norm_0": 0.5})
	if err != nil {
		t.Fatalf("Run() 出错：%v", err)
	}
	if got := value.(float64); got != 0.6*0.5+0.4*10 {
		t.Fatalf("score 不符：%v", got)
	}
	entry, err := CompileExpression("bars[-1].close <= bars[-1].ma_20 && bars[0].close > bars[0].ma_20 && instrument_id == \"BTC-USDT\"", StageEntry, columns)
	if err != nil {
		t.Fatalf("CompileExpression(entry) 出错：%v", err)
	}
	ok, err := entry.Run(map[string]any{"close": 11.0, "ma_20": 10.0, "bias_q_20": 0.0, "bars": []map[string]float64{{"close": 11, "ma_20": 10}, {"close": 9, "ma_20": 10}}, "score": 0.0, "instrument_id": "BTC-USDT"})
	if err != nil {
		t.Fatalf("Run(entry) 出错：%v", err)
	}
	if ok != true {
		t.Fatalf("entry 结果不符：%v", ok)
	}
	where, err := CompileExpression("score > 0.5", StageSelectWhere, columns)
	if err != nil {
		t.Fatalf("CompileExpression(where) 出错：%v", err)
	}
	if value, err := where.Run(map[string]any{"close": 1.0, "ma_20": 1.0, "bias_q_20": 1.0, "bars": []map[string]float64{{}, {}}, "score": 0.7, "instrument_id": ""}); err != nil || value != true {
		t.Fatalf("where = %v，错误 = %v", value, err)
	}
	integerScore, err := CompileExpression("close > 1 ? 1 : 0", StageScore, columns)
	if err != nil {
		t.Fatalf("CompileExpression(整数 score) 出错：%v", err)
	}
	if value, err := integerScore.Run(map[string]any{"close": 2.0, "ma_20": 0.0, "bias_q_20": 0.0, "bars": []map[string]float64{{}, {}}, "score": 0.0, "instrument_id": ""}); err != nil || value.(float64) != 1 {
		t.Fatalf("整数 score = %v（%T），错误 = %v", value, value, err)
	}
}

func TestCompileExpressionRejectsUnknownColumns(t *testing.T) {
	t.Parallel()
	if _, err := CompileExpression("close > turnover_20", StageFilter, columnSet("close")); err == nil || !strings.Contains(err.Error(), "turnover_20") {
		t.Fatalf("错误不符：%v", err)
	}
	if _, err := CompileExpression("rank(turnover_20)", StageScore, columnSet("close")); err == nil || !strings.Contains(err.Error(), "turnover_20") {
		t.Fatalf("错误不符：%v", err)
	}
	if _, err := CompileExpression("bars[-1].turnover_20 > 0", StageFilter, columnSet("close")); err == nil || !strings.Contains(err.Error(), "turnover_20") {
		t.Fatalf("错误不符：%v", err)
	}
	if _, err := CompileExpression("close + instrument_id", StageScore, columnSet("close")); err == nil {
		t.Fatal("不应接受字符串运算")
	}
}

func TestCompileProgramFromExample(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(exampleDSL))
	if err != nil {
		t.Fatalf("Parse() 出错：%v", err)
	}
	program, err := Compile(strategy, exampleColumns)
	if err != nil {
		t.Fatalf("Compile() 出错：%v", err)
	}
	if !program.UsesPreviousBar || !reflect.DeepEqual(program.PreviousColumns, []string{"close", "ma_20"}) {
		t.Fatalf("上一根列不符：%v", program.PreviousColumns)
	}
	if !reflect.DeepEqual(program.Columns, []string{"bias_q_20", "close", "ma_20", "quote_volume_mean_20", "quote_volume_mean_q_20"}) {
		t.Fatalf("当期列不符：%v", program.Columns)
	}
	rank := program.Rules[0]
	if rank.Filter == nil || rank.Score == nil || rank.SelectWhere != nil || len(rank.Score.Normalizers) != 2 {
		t.Fatalf("rank 规则不符：%+v", rank)
	}
	signal := program.Rules[1]
	if signal.Entry == nil || signal.Exit == nil || !reflect.DeepEqual(signal.PreviousColumns(), []string{"close", "ma_20"}) {
		t.Fatalf("signal 规则不符：%+v", signal)
	}
	if _, err := Compile(strategy, []string{"close"}); err == nil || !strings.Contains(err.Error(), "long_momentum") {
		t.Fatalf("缺列时 Compile() 的错误不符：%v", err)
	}
	referenced, err := ReferencedColumns(strategy)
	if err != nil || !reflect.DeepEqual(referenced, program.Columns) {
		t.Fatalf("ReferencedColumns() = %v，错误 = %v", referenced, err)
	}
}
