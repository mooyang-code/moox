package factorname

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

type vectorFile struct {
	FormatValue []struct {
		Value float64 `json:"value"`
		Text  string  `json:"text"`
	} `json:"format_value"`
	FixedKey []struct {
		Name     string             `json:"name"`
		FactorID string             `json:"factor_id"`
		Decl     []vectorParam      `json:"decl"`
		Values   map[string]float64 `json:"values"`
		Key      string             `json:"key"`
		Error    bool               `json:"error"`
	} `json:"fixed_key"`
	VarKey []struct {
		FactorID string `json:"factor_id"`
		Key      string `json:"key"`
		Error    bool   `json:"error"`
	} `json:"var_key"`
	Column []struct {
		Key    string `json:"key"`
		Output string `json:"output"`
		Column string `json:"column"`
		Error  bool   `json:"error"`
	} `json:"column"`
	OutputName []struct {
		FactorID string `json:"factor_id"`
		Name     string `json:"name"`
		OK       bool   `json:"ok"`
	} `json:"output_name"`
	Outputs []struct {
		FactorID string   `json:"factor_id"`
		Outputs  []string `json:"outputs"`
		OK       bool     `json:"ok"`
	} `json:"outputs"`
	ParseColumn []struct {
		Column    string    `json:"column"`
		OK        bool      `json:"ok"`
		FactorID  string    `json:"factor_id"`
		Kind      string    `json:"kind"`
		HasParams bool      `json:"has_params"`
		Values    []float64 `json:"values"`
		Output    string    `json:"output"`
	} `json:"parse_column"`
	CanonicalParams []struct {
		Input map[string]map[string]float64 `json:"input"`
		Text  string                        `json:"text"`
		Error bool                          `json:"error"`
	} `json:"canonical_params"`
	CanonicalValues []struct {
		Input map[string]float64 `json:"input"`
		Text  string             `json:"text"`
		Error bool               `json:"error"`
	} `json:"canonical_values"`
	ID []struct {
		Suffix  string `json:"suffix"`
		Task    string `json:"task"`
		Dataset string `json:"dataset"`
		View    string `json:"view"`
	} `json:"id"`
	SuffixInvalid  []string `json:"suffix_invalid"`
	IDParseInvalid []string `json:"id_parse_invalid"`
}

type vectorParam struct {
	Name    string  `json:"name"`
	Default float64 `json:"default"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectorFile
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func toParams(in []vectorParam) []Param {
	out := make([]Param, len(in))
	for i, p := range in {
		out[i] = Param{Name: p.Name, Default: p.Default}
	}
	return out
}

func TestFormatValueVectors(t *testing.T) {
	for _, c := range loadVectors(t).FormatValue {
		got, err := FormatValue(c.Value)
		if err != nil || got != c.Text {
			t.Errorf("FormatValue(%v) = %q, %v；期望 %q", c.Value, got, err, c.Text)
		}
		// 有限数写出的片段必须能原样解析回同一个数
		if c.Value != 0 {
			back, err := ParseValue(got)
			if err != nil || back != c.Value {
				t.Errorf("ParseValue(%q) = %v, %v；期望 %v", got, back, err, c.Value)
			}
		}
	}
}

func TestFormatValueRejectsNonFinite(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := FormatValue(v); err == nil {
			t.Errorf("FormatValue(%v) 应当报错", v)
		}
	}
}

func TestFixedKeyVectors(t *testing.T) {
	for _, c := range loadVectors(t).FixedKey {
		got, err := FixedKey(c.FactorID, toParams(c.Decl), c.Values)
		if c.Error {
			if err == nil {
				t.Errorf("%s：期望报错，得到 %q", c.Name, got)
			}
			continue
		}
		if err != nil || got != c.Key {
			t.Errorf("%s：得到 %q, %v；期望 %q", c.Name, got, err, c.Key)
		}
	}
}

func TestVarKeyVectors(t *testing.T) {
	for _, c := range loadVectors(t).VarKey {
		got, err := VarKey(c.FactorID)
		if c.Error != (err != nil) || (!c.Error && got != c.Key) {
			t.Errorf("VarKey(%q) = %q, %v；期望 %q（报错=%v）", c.FactorID, got, err, c.Key, c.Error)
		}
	}
}

func TestColumnVectors(t *testing.T) {
	for _, c := range loadVectors(t).Column {
		got, err := Column(c.Key, c.Output)
		if c.Error != (err != nil) || (!c.Error && got != c.Column) {
			t.Errorf("Column(%q, %q) = %q, %v；期望 %q（报错=%v）", c.Key, c.Output, got, err, c.Column, c.Error)
		}
	}
}

func TestOutputVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.OutputName {
		if err := ValidateOutputName(c.FactorID, c.Name); (err == nil) != c.OK {
			t.Errorf("ValidateOutputName(%q, %q) = %v；期望通过=%v", c.FactorID, c.Name, err, c.OK)
		}
	}
	for _, c := range v.Outputs {
		if err := ValidateOutputs(c.FactorID, c.Outputs); (err == nil) != c.OK {
			t.Errorf("ValidateOutputs(%q, %v) = %v；期望通过=%v", c.FactorID, c.Outputs, err, c.OK)
		}
	}
}

func TestParseColumnVectors(t *testing.T) {
	for _, c := range loadVectors(t).ParseColumn {
		got, err := ParseColumn(c.Column)
		if !c.OK {
			if err == nil {
				t.Errorf("ParseColumn(%q) 应当报错，得到 %+v", c.Column, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseColumn(%q)：%v", c.Column, err)
			continue
		}
		if got.FactorID != c.FactorID || got.Kind != c.Kind || got.HasParams != c.HasParams || got.Output != c.Output || len(got.Values) != len(c.Values) {
			t.Errorf("ParseColumn(%q) = %+v；期望 %+v", c.Column, got, c)
			continue
		}
		for i := range c.Values {
			if got.Values[i] != c.Values[i] {
				t.Errorf("ParseColumn(%q) 第 %d 个值 %v；期望 %v", c.Column, i, got.Values[i], c.Values[i])
			}
		}
	}
}

func TestCanonicalVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.CanonicalParams {
		got, err := CanonicalParams(c.Input)
		if c.Error != (err != nil) || (!c.Error && got != c.Text) {
			t.Errorf("CanonicalParams(%v) = %q, %v；期望 %q（报错=%v）", c.Input, got, err, c.Text, c.Error)
		}
		if !c.Error {
			if _, err := ParseParams(got); err != nil {
				t.Errorf("ParseParams(%q)：%v", got, err)
			}
		}
	}
	for _, c := range v.CanonicalValues {
		got, err := CanonicalValues(c.Input)
		if c.Error != (err != nil) || (!c.Error && got != c.Text) {
			t.Errorf("CanonicalValues(%v) = %q, %v；期望 %q（报错=%v）", c.Input, got, err, c.Text, c.Error)
		}
	}
}

func TestIDVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.ID {
		task, err1 := TaskID(c.Suffix)
		dataset, err2 := ResultDatasetID(c.Suffix)
		view, err3 := ResultViewID(c.Suffix)
		if err1 != nil || err2 != nil || err3 != nil || task != c.Task || dataset != c.Dataset || view != c.View {
			t.Errorf("后缀 %q：得到 %q %q %q（%v %v %v）", c.Suffix, task, dataset, view, err1, err2, err3)
		}
		for _, parse := range []struct {
			id string
			fn func(string) (string, error)
		}{{task, ParseTaskID}, {dataset, ParseResultDatasetID}, {view, ParseResultViewID}} {
			if got, err := parse.fn(parse.id); err != nil || got != c.Suffix {
				t.Errorf("反解 %q = %q, %v；期望 %q", parse.id, got, err, c.Suffix)
			}
		}
		if len(dataset) > 50 {
			t.Errorf("结果数据集 ID %q 超过 Storage 的 50 个字符上限", dataset)
		}
	}
	for _, suffix := range v.SuffixInvalid {
		if err := ValidateSuffix(suffix); err == nil {
			t.Errorf("后缀 %q 应当不合法", suffix)
		}
	}
	for _, id := range v.IDParseInvalid {
		if _, err := ParseTaskID(id); err == nil {
			t.Errorf("ParseTaskID(%q) 应当报错", id)
		}
	}
}

func TestNewSuffixIsValid(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		suffix, err := NewSuffix()
		if err != nil || len(suffix) != generatedLen || ValidateSuffix(suffix) != nil {
			t.Fatalf("NewSuffix() = %q, %v", suffix, err)
		}
		seen[suffix] = true
	}
	if len(seen) < 45 {
		t.Errorf("50 个随机后缀只有 %d 个不重复", len(seen))
	}
}

func TestKeyColumnRoundTrip(t *testing.T) {
	ema := []Param{{"span", 20}, {"warmup", 0}}
	for _, given := range []map[string]float64{nil, {"span": 30}, {"span": 20, "warmup": 50000}, {"span": 2.5, "warmup": 0}} {
		key, err := FixedKey("ema", ema, given)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseMemberKey(key)
		if err != nil || parsed.FactorID != "ema" || parsed.Kind != KindFixed {
			t.Fatalf("ParseMemberKey(%q) = %+v, %v", key, parsed, err)
		}
		params, err := parsed.ParamMap(ema)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ema {
			want := p.Default
			if v, ok := given[p.Name]; ok {
				want = v
			}
			if params[p.Name] != want {
				t.Errorf("%q 还原 %s=%v；期望 %v", key, p.Name, params[p.Name], want)
			}
		}
	}
	// 多输出：逐列拼出再解析回来
	columns, err := Columns("boll", "boll__30", []string{"mid", "up", "low", "pctb"})
	if err != nil || strings.Join(columns, ",") != "boll__30__mid,boll__30__up,boll__30__low,boll__30__pctb" {
		t.Fatalf("Columns = %v, %v", columns, err)
	}
	for i, column := range columns {
		parsed, err := ParseColumn(column)
		if err != nil || parsed.FactorID != "boll" || parsed.Output != []string{"mid", "up", "low", "pctb"}[i] {
			t.Errorf("ParseColumn(%q) = %+v, %v", column, parsed, err)
		}
	}
	// 单输出：列名就是成员键
	if columns, err := Columns("ema", "ema__20_50000", []string{"ema"}); err != nil || len(columns) != 1 || columns[0] != "ema__20_50000" {
		t.Errorf("单输出 Columns = %v, %v", columns, err)
	}
}

func TestParamMapChecksDeclaredCount(t *testing.T) {
	parsed, err := ParseMemberKey("ema__20_50000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parsed.ParamMap([]Param{{"span", 20}}); err == nil {
		t.Error("参数个数与定义不一致时应当报错")
	}
	varKey, _ := ParseMemberKey("ma_fast__var")
	if m, err := varKey.ParamMap([]Param{{"window", 5}}); err != nil || m != nil {
		t.Errorf("可变成员没有参数值：%v, %v", m, err)
	}
}

func TestParamsColumnIsNotAFactorColumn(t *testing.T) {
	if _, err := ParseColumn(ParamsColumn); err == nil {
		t.Error("__params 不是因子值列，ParseColumn 应当拒绝")
	}
}

func TestVectorsAreLoaded(t *testing.T) {
	v := loadVectors(t)
	for name, n := range map[string]int{
		"format_value": len(v.FormatValue), "fixed_key": len(v.FixedKey), "var_key": len(v.VarKey), "column": len(v.Column),
		"output_name": len(v.OutputName), "outputs": len(v.Outputs), "parse_column": len(v.ParseColumn),
		"canonical_params": len(v.CanonicalParams), "canonical_values": len(v.CanonicalValues), "id": len(v.ID),
		"suffix_invalid": len(v.SuffixInvalid), "id_parse_invalid": len(v.IDParseInvalid),
	} {
		if n == 0 {
			t.Errorf("测试向量 %s 为空：向量文件的字段名与测试不一致？", name)
		}
	}
}
