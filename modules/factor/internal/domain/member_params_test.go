package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func compiledDef(t *testing.T, id, spec string) FactorDef {
	t.Helper()
	def, err := CompileDefinition(FactorDef{FactorID: id, Name: id, Spec: spec, SpecHash: SpecHash(spec)})
	require.NoError(t, err)
	return def
}

func TestDeriveMemberKey(t *testing.T) {
	ema := compiledDef(t, "ema", "params:\n  span: {type: int, min: 2, max: 4825, default: 20}\n  warmup: {type: int, min: 0, max: 50000, default: 0}\noutput: ema(close, span, warmup)\n")
	for name, tc := range map[string]struct {
		kind   string
		params map[string]float64
		want   string
	}{
		"全默认":   {MemberKindFixed, nil, "ema"},
		"带参数段":  {MemberKindFixed, map[string]float64{"span": 30}, "ema__30_0"},
		"预热非默认": {MemberKindFixed, map[string]float64{"warmup": 50000}, "ema__20_50000"},
		"可变成员":  {MemberKindVar, map[string]float64{"span": 30}, "ema__var"},
	} {
		key, err := DeriveMemberKey(ema, tc.kind, tc.params)
		require.NoError(t, err, name)
		require.Equal(t, tc.want, key, name)
	}
	_, err := DeriveMemberKey(ema, "other", nil)
	require.ErrorContains(t, err, "成员类型")
	_, err = DeriveMemberKey(ema, MemberKindFixed, map[string]float64{"nope": 1})
	require.Error(t, err)
}

func TestResolveMemberParams(t *testing.T) {
	ema := compiledDef(t, "ema", "params:\n  span: {type: int, min: 2, max: 4825, default: 20}\n  warmup: {type: int, min: 0, max: 50000, default: 0}\noutput: ema(close, span, warmup)\n")
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

	plain := compiledDef(t, "plain", "output: close\n")
	full, err = ResolveMemberParams(plain, nil)
	require.NoError(t, err)
	require.Nil(t, full)
	_, err = ResolveMemberParams(plain, map[string]float64{"x": 1})
	require.Error(t, err)
}

func TestMemberSnapshot(t *testing.T) {
	ema := compiledDef(t, "ema", "params:\n  span: {type: int, min: 2, max: 4825, default: 20}\n  warmup: {type: int, min: 0, max: 50000, default: 0}\noutput: ema(close, span, warmup)\n")

	plain := MemberDef{Key: "ema", Factor: ema, Kind: MemberKindFixed}
	require.Equal(t, 20.0, MemberSnapshot(plain).Value("span", "BTC"))
	require.Equal(t, 0.0, MemberSnapshot(plain).Value("warmup", "BTC"))

	fixed := MemberDef{Key: "ema__20_50000", Factor: ema, Kind: MemberKindFixed, Params: map[string]float64{"span": 20, "warmup": 50000}}
	require.Equal(t, []string{"ema__20_50000"}, fixed.OutputColumns())
	snapshot := MemberSnapshot(fixed)
	require.Equal(t, 50000.0, snapshot.Value("warmup", "BTC"))
	require.Equal(t, 20.0, snapshot.Value("span", "ETH"))
	require.NotEqual(t, MemberSnapshot(plain).Hash(), snapshot.Hash())

	variable := MemberDef{Key: "ema__var", Factor: ema, Kind: MemberKindVar, Params: map[string]float64{"span": 25, "warmup": 100}}
	require.True(t, variable.IsVar())
	require.Equal(t, 25.0, MemberSnapshot(variable).Value("span", "BTC"), "可变成员的默认参数就是没有提交时的取值")
}

func TestCheckMemberParamsAfterDefinitionChange(t *testing.T) {
	narrow := compiledDef(t, "ema", "params:\n  span: {type: int, min: 2, max: 30, default: 20}\noutput: ema(close, span)\n")
	require.NoError(t, CheckMemberParams(narrow, map[string]float64{"span": 20}))
	require.Error(t, CheckMemberParams(narrow, map[string]float64{"span": 50}), "超出新范围")

	require.ErrorContains(t, CheckMemberParams(narrow, nil), "新增了参数", "定义现在声明了参数，空的参数不完整")
	require.ErrorContains(t, CheckMemberParams(narrow, map[string]float64{"span": 20, "extra": 1}), "不再声明")

	wider := compiledDef(t, "ema", "params:\n  span: {type: int, min: 2, max: 30, default: 20}\n  warmup: {type: int, min: 0, max: 100, default: 0}\noutput: ema(close, span, warmup)\n")
	require.ErrorContains(t, CheckMemberParams(wider, map[string]float64{"span": 20}), "新增了参数 warmup", "定义新增参数后旧成员要重加")

	plain := compiledDef(t, "plain", "output: close\n")
	require.NoError(t, CheckMemberParams(plain, nil))
	require.ErrorContains(t, CheckMemberParams(plain, map[string]float64{"span": 20}), "不再声明")
}

func TestParamsSummary(t *testing.T) {
	require.Equal(t, "", ParamsSummary(nil))
	require.Equal(t, "span=20, warmup=50000", ParamsSummary(map[string]float64{"warmup": 50000, "span": 20}))
}
