package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func smaMember(t *testing.T) MemberDef {
	t.Helper()
	def := compiledFactor(t, "sma", "params:\n  n: {type: int, min: 2, max: 100, default: 20}\n  m: {type: int, min: 1, max: 99, default: 2}\n  k: {type: float, min: 0, max: 5, default: 1}\noutput: sma(close, n, m) * k\n")
	return MemberDef{Key: "sma__var", Factor: def, Kind: MemberKindVar}
}

func TestDefaultSnapshotAndValue(t *testing.T) {
	member := smaMember(t)
	snapshot := DefaultSnapshot(member.Factor)
	require.Equal(t, ParamSnapshot{"n": {Default: 20}, "m": {Default: 2}, "k": {Default: 1}}, snapshot)
	snapshot["n"] = ParamValue{Default: 30, Overrides: map[string]float64{"BTC": 10}}
	require.Equal(t, 10.0, snapshot.Value("n", "BTC"))
	require.Equal(t, 30.0, snapshot.Value("n", "ETH"))
	require.NoError(t, ValidateSnapshot(member, snapshot, func(string) bool { return true }))
}

// 摘要只取决于内容：覆盖为空与省略覆盖相同，键的顺序无关。
func TestSnapshotHashCanonical(t *testing.T) {
	a := ParamSnapshot{"n": {Default: 20, Overrides: map[string]float64{}}, "m": {Default: 2}}
	b := ParamSnapshot{"m": {Default: 2}, "n": {Default: 20}}
	require.Equal(t, a.Hash(), b.Hash())
	c := ParamSnapshot{"m": {Default: 2}, "n": {Default: 20, Overrides: map[string]float64{"BTC": 10}}}
	require.NotEqual(t, a.Hash(), c.Hash())
	parsed, err := ParseParamSnapshot(c.Canonical())
	require.NoError(t, err)
	require.Equal(t, c.Hash(), parsed.Hash())
}

func TestValidateSnapshotRejects(t *testing.T) {
	member := smaMember(t)
	base := func() ParamSnapshot { return DefaultSnapshot(member.Factor) }
	inScope := func(subject string) bool { return subject != "SOL" }
	cases := map[string]func(ParamSnapshot){
		"missing param":   func(s ParamSnapshot) { delete(s, "k") },
		"unknown param":   func(s ParamSnapshot) { s["x"] = ParamValue{Default: 1} },
		"out of range":    func(s ParamSnapshot) { s["n"] = ParamValue{Default: 101} },
		"not integer":     func(s ParamSnapshot) { s["n"] = ParamValue{Default: 20.5} },
		"joint default":   func(s ParamSnapshot) { s["m"] = ParamValue{Default: 20} },
		"joint override":  func(s ParamSnapshot) { s["m"] = ParamValue{Default: 2, Overrides: map[string]float64{"BTC": 30}} },
		"out of scope":    func(s ParamSnapshot) { s["k"] = ParamValue{Default: 1, Overrides: map[string]float64{"SOL": 2}} },
		"blank subject":   func(s ParamSnapshot) { s["k"] = ParamValue{Default: 1, Overrides: map[string]float64{" ": 2}} },
		"override range":  func(s ParamSnapshot) { s["k"] = ParamValue{Default: 1, Overrides: map[string]float64{"BTC": 9}} },
		"joint per merge": func(s ParamSnapshot) { s["n"] = ParamValue{Default: 20, Overrides: map[string]float64{"BTC": 2}} },
	}
	for name, mutate := range cases {
		snapshot := base()
		mutate(snapshot)
		require.Error(t, ValidateSnapshot(member, snapshot, inScope), name)
	}
}

// 按标的合并：默认值取本次的；本次带了的标的覆盖旧值，没带的标的保留旧值。
func TestMergeSnapshots(t *testing.T) {
	previous := ParamSnapshot{
		"n": {Default: 20, Overrides: map[string]float64{"BTC": 10, "ETH": 15}},
		"k": {Default: 1},
	}
	submitted := ParamSnapshot{
		"n": {Default: 30, Overrides: map[string]float64{"BTC": 12, "SOL": 8}},
		"k": {Default: 2},
	}
	merged := MergeSnapshots(previous, submitted)
	require.Equal(t, ParamSnapshot{
		"n": {Default: 30, Overrides: map[string]float64{"BTC": 12, "ETH": 15, "SOL": 8}},
		"k": {Default: 2},
	}, merged)
	// 不改动入参。
	require.Equal(t, 10.0, previous["n"].Overrides["BTC"])
	require.Len(t, submitted["n"].Overrides, 2)
	// 旧快照里本次没有的参数被丢弃；从空开始合并就是本次提交本身。
	require.Equal(t, submitted.Canonical(), MergeSnapshots(nil, submitted).Canonical())
	require.NotContains(t, MergeSnapshots(ParamSnapshot{"gone": {Default: 1}}, submitted), "gone")
}

func TestCheckDefinitionCompatible(t *testing.T) {
	spec := func(span, k string) string {
		return "params:\n  span: " + span + "\n  k: " + k + "\noutput: ts_mean(close, span) * k\n"
	}
	const spanOK = "{type: int, min: 2, max: 100, default: 20}"
	const kOK = "{type: float, min: 0, max: 5, default: 1}"
	base := compiledFactor(t, "ema", spec(spanOK, kOK))
	compat := func(text string) error {
		return CheckDefinitionCompatible(base, compiledFactor(t, "ema", text))
	}
	// 表达式与范围可以改。
	require.NoError(t, compat(spec("{type: int, min: 2, max: 200, default: 20}", kOK)))
	require.NoError(t, compat("params:\n  span: "+spanOK+"\n  k: "+kOK+"\noutput: ts_mean(close, span) * k + 1\n"))
	for name, text := range map[string]string{
		"增参数":  "params:\n  span: " + spanOK + "\n  k: " + kOK + "\n  j: {type: float, min: 0, max: 5, default: 1}\noutput: ts_mean(close, span) * k * j\n",
		"删参数":  "params:\n  span: " + spanOK + "\noutput: ts_mean(close, span)\n",
		"换顺序":  "params:\n  k: " + kOK + "\n  span: " + spanOK + "\noutput: ts_mean(close, span) * k\n",
		"改默认值": spec("{type: int, min: 2, max: 100, default: 21}", kOK),
		"改类型":  spec(spanOK, "{type: int, min: 0, max: 5, default: 1}"),
		"改参数名": "params:\n  length: " + spanOK + "\n  k: " + kOK + "\noutput: ts_mean(close, length) * k\n",
	} {
		require.Error(t, compat(text), name)
	}
	// 输出改变：单输出改多输出，多输出增删、改名、重排。
	multi := compiledFactor(t, "boll", "params: {w: {type: int, min: 2, max: 50, default: 20}}\noutputs:\n  up: ts_mean(close, w)\n  low: ts_mean(close, w)\n")
	require.Error(t, CheckDefinitionCompatible(multi, compiledFactor(t, "boll", "params: {w: {type: int, min: 2, max: 50, default: 20}}\noutputs:\n  low: ts_mean(close, w)\n  up: ts_mean(close, w)\n")))
	require.Error(t, CheckDefinitionCompatible(multi, compiledFactor(t, "boll", "params: {w: {type: int, min: 2, max: 50, default: 20}}\noutputs:\n  up: ts_mean(close, w)\n  mid: ts_mean(close, w)\n  low: ts_mean(close, w)\n")))
	require.NoError(t, CheckDefinitionCompatible(multi, compiledFactor(t, "boll", "params: {w: {type: int, min: 2, max: 80, default: 20}}\noutputs:\n  up: ts_mean(close, w) + 1\n  low: ts_mean(close, w)\n")))
}
