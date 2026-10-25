package domain

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type aliasVector struct {
	Name     string             `json:"name"`
	FactorID string             `json:"factor_id"`
	Spec     string             `json:"spec"`
	Params   map[string]float64 `json:"params"`
	Alias    string             `json:"alias"`
	Error    string             `json:"error"`
}

func compiledDef(t *testing.T, id, spec string) FactorDef {
	t.Helper()
	def, err := CompileDefinition(FactorDef{FactorID: id, Name: id, Spec: spec, SpecHash: SpecHash(spec)})
	require.NoError(t, err)
	return def
}

// 别名生成的测试向量与前端共用（web 的别名预览读同一份文件）。
func TestDeriveMemberAliasVectors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/alias_vectors.json")
	require.NoError(t, err)
	var vectors []aliasVector
	require.NoError(t, json.Unmarshal(raw, &vectors))
	require.NotEmpty(t, vectors)
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			def := compiledDef(t, vector.FactorID, vector.Spec)
			alias, err := DeriveMemberAlias(def, vector.Params)
			if vector.Error != "" {
				require.ErrorContains(t, err, vector.Error)
				return
			}
			require.NoError(t, err)
			require.Equal(t, vector.Alias, alias)
			if alias != vector.FactorID {
				require.NoError(t, ValidateAlias(alias))
			}
		})
	}
}

func TestFormatParamValue(t *testing.T) {
	for value, want := range map[float64]string{
		20: "20", 0.5: "0p5", -1: "n1", -0.25: "n0p25", 1e-7: "0p0000001", 123456789: "123456789", math.Copysign(0, -1): "0",
	} {
		require.Equal(t, want, FormatParamValue(value), "%v", value)
	}
	require.False(t, strings.ContainsAny(FormatParamValue(1e21), "e+"))
	// 前端用同样的数字：极小与极大值都按十进制展开（见 web 的 member-params.test.ts）。
	for value, want := range map[float64]string{
		1e21: "1000000000000000000000", 1e-21: "0p000000000000000000001", 1.5e-10: "0p00000000015", -2.5e-8: "n0p000000025", 1.25e22: "12500000000000000000000",
	} {
		require.Equal(t, want, FormatParamValue(value), "%v", value)
	}
}

func TestResolveMemberParams(t *testing.T) {
	ema := compiledDef(t, "Ema", "params:\n  span: {type: int, min: 2, max: 4825, default: 20}\n  warmup: {type: int, min: 0, max: 50000, default: 0}\noutputs:\n  ema: ema(close, span, warmup)\n")
	full, err := ResolveMemberParams(ema, map[string]float64{"warmup": 50000})
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"span": 20, "warmup": 50000}, full)

	full, err = ResolveMemberParams(ema, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"span": 20, "warmup": 0}, full)

	for name, given := range map[string]map[string]float64{
		"未声明的参数": {"nope": 1}, "越界": {"span": 1}, "整数参数给了小数": {"span": 20.5}, "预热超上限": {"warmup": 50001},
	} {
		_, err := ResolveMemberParams(ema, given)
		require.Error(t, err, name)
	}

	plain := compiledDef(t, "Plain", "outputs:\n  o: close\n")
	full, err = ResolveMemberParams(plain, nil)
	require.NoError(t, err)
	require.Nil(t, full)
	_, err = ResolveMemberParams(plain, map[string]float64{"x": 1})
	require.Error(t, err)
}

func TestMemberSnapshotAndParamColumns(t *testing.T) {
	ema := compiledDef(t, "Ema", "params:\n  span: {type: int, min: 2, max: 4825, default: 20}\n  warmup: {type: int, min: 0, max: 50000, default: 0}\noutputs:\n  ema: ema(close, span, warmup)\n")

	dynamic := MemberDef{Alias: "Ema", Factor: ema}
	require.False(t, dynamic.FixedParams())
	require.Equal(t, []string{"Ema__span", "Ema__warmup"}, dynamic.ParamColumns())
	require.Equal(t, 20.0, MemberSnapshot(dynamic).Value("span", "BTC"))
	require.Equal(t, 0.0, MemberSnapshot(dynamic).Value("warmup", "BTC"))

	fixed := MemberDef{Alias: "Ema_20_50000", Factor: ema, Params: map[string]float64{"span": 20, "warmup": 50000}}
	require.True(t, fixed.FixedParams())
	require.Empty(t, fixed.ParamColumns())
	require.Equal(t, []string{"Ema_20_50000_ema"}, fixed.OutputColumns())
	snapshot := MemberSnapshot(fixed)
	require.Equal(t, 50000.0, snapshot.Value("warmup", "BTC"))
	require.Equal(t, 20.0, snapshot.Value("span", "ETH"))
	require.NotEqual(t, ParamsDigest(MemberSnapshot(dynamic)), ParamsDigest(snapshot))
}

func TestCheckSharedMemberParamsAfterDefinitionChange(t *testing.T) {
	narrow := compiledDef(t, "Ema", "params:\n  span: {type: int, min: 2, max: 30, default: 20}\noutputs:\n  ema: ema(close, span)\n")
	require.NoError(t, CheckSharedMemberParams(narrow, map[string]float64{"span": 20}))
	require.Error(t, CheckSharedMemberParams(narrow, map[string]float64{"span": 50}), "超出新范围")

	require.ErrorContains(t, CheckSharedMemberParams(narrow, nil), "新增了参数", "定义现在声明了参数，空的固定参数不完整")
	require.ErrorContains(t, CheckSharedMemberParams(narrow, map[string]float64{"span": 20, "extra": 1}), "不再声明")

	wider := compiledDef(t, "Ema", "params:\n  span: {type: int, min: 2, max: 30, default: 20}\n  warmup: {type: int, min: 0, max: 100, default: 0}\noutputs:\n  ema: ema(close, span, warmup)\n")
	require.ErrorContains(t, CheckSharedMemberParams(wider, map[string]float64{"span": 20}), "新增了参数 warmup", "定义新增参数后旧成员要重加")

	plain := compiledDef(t, "Plain", "outputs:\n  o: close\n")
	require.NoError(t, CheckSharedMemberParams(plain, nil))
	require.ErrorContains(t, CheckSharedMemberParams(plain, map[string]float64{"span": 20}), "不再声明")
}

func TestParamsSummary(t *testing.T) {
	require.Equal(t, "", ParamsSummary(nil))
	require.Equal(t, "span=20, warmup=50000", ParamsSummary(map[string]float64{"warmup": 50000, "span": 20}))
}
