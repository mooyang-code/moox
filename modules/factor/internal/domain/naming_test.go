package domain

import (
	"testing"
	"time"
)

func baseFactor() FactorDef {
	return FactorDef{
		FactorID: "mom", Name: "动量", FactorType: "single_symbol_timeseries", SourceCode: "def compute(x):\n    return x\n",
		InputColumns: []string{"close"}, Outputs: []string{"mom"}, ParamsJSON: `{"window":20,"scale":1.5}`,
		LookbackPeriods: 21, AllowPartialUniverse: false,
	}
}

// DefinitionHash 是完整计算契约的指纹：源码、类型、参数、回看周期、输入输出列、allow_partial_universe 任一变化都要变；
// 只改参数而不改源码时 SourceHash 不变、DefinitionHash 变化。
func TestDefinitionHashChangesWithEveryContractField(t *testing.T) {
	base := baseFactor()
	want := DefinitionHash(base)
	if DefinitionHash(base) != want {
		t.Fatal("同一定义的指纹必须稳定")
	}
	changes := map[string]func(*FactorDef){
		"源码":                     func(f *FactorDef) { f.SourceCode += "# changed\n" },
		"因子类型":                   func(f *FactorDef) { f.FactorType = "cross_section" },
		"参数值":                    func(f *FactorDef) { f.ParamsJSON = `{"window":30,"scale":1.5}` },
		"参数新增键":                  func(f *FactorDef) { f.ParamsJSON = `{"window":20,"scale":1.5,"extra":1}` },
		"回看周期":                   func(f *FactorDef) { f.LookbackPeriods = 22 },
		"输入列":                    func(f *FactorDef) { f.InputColumns = []string{"close", "volume"} },
		"输出列":                    func(f *FactorDef) { f.Outputs = []string{"mom", "mom2"} },
		"allow_partial_universe": func(f *FactorDef) { f.AllowPartialUniverse = true },
	}
	for name, change := range changes {
		changed := baseFactor()
		change(&changed)
		if DefinitionHash(changed) == want {
			t.Errorf("%s 变化后指纹应不同", name)
		}
	}
	// 只改参数：源码 hash 不变，定义指纹变化。
	params := baseFactor()
	params.ParamsJSON = `{"window":30,"scale":1.5}`
	if SourceHash(params.SourceCode) != SourceHash(base.SourceCode) || DefinitionHash(params) == want {
		t.Error("只改参数时 SourceHash 不变、DefinitionHash 应变化")
	}
}

// 不属于计算契约的字段（标识、名称、时间戳）和参数的书写形式（键序、空白）不影响指纹；预先算好的 SourceHash 与现算一致。
func TestDefinitionHashIgnoresNonContractFieldsAndNormalizesParams(t *testing.T) {
	base := baseFactor()
	want := DefinitionHash(base)
	same := baseFactor()
	same.FactorID, same.Name = "other", "另一个名字"
	same.CreatedAt, same.UpdatedAt = time.Unix(1, 0), time.Unix(2, 0)
	same.ParamsJSON = "{ \"scale\": 1.5,\n  \"window\": 20 }"
	same.SourceHash = SourceHash(same.SourceCode)
	if got := DefinitionHash(same); got != want {
		t.Fatalf("标识、名称、时间戳、参数书写形式与键序不应影响指纹：%s != %s", got, want)
	}
	empty := baseFactor()
	empty.ParamsJSON = ""
	braces := baseFactor()
	braces.ParamsJSON = "{}"
	if DefinitionHash(empty) != DefinitionHash(braces) {
		t.Error("空参数与 {} 应规范化为同一指纹")
	}
}
