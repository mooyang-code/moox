package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func smaMember(t *testing.T) MemberDef {
	t.Helper()
	def := compiledFactor(t, "sma", "params:\n  n: {type: int, min: 2, max: 100, default: 20}\n  m: {type: int, min: 1, max: 99, default: 2}\n  k: {type: float, min: 0, max: 5, default: 1}\noutput: sma(close, n, m) * k\n")
	return MemberDef{Alias: "fast", Factor: def}
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
