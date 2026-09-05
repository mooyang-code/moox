package compiler

import (
	"context"
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/stretchr/testify/require"
)

func compileFields() map[string]reflect.Type {
	return map[string]reflect.Type{
		"ma20":        reflect.TypeOf(float64(0)),
		"return_20":   reflect.TypeOf(float64(0)),
		"turnover_20": reflect.TypeOf(float64(0)),
	}
}

func TestCompileDSL(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: demo
triggers: {event: {name: ready}}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  ranked:
    pool: {udf: spot_symbols}
    filter_before: "turnover_20 > 1000000"
    score: "0.6 * pct_rank(return_20) + 0.4 * zscore(turnover_20)"
    select: {where: "score >= 0.6", top: 5}
    weight: 0.6
    filter_after: "return_20 > 0"
  signal:
    pool: [BTC-USDT-SPOT]
    signals: {entry: "bars[0].ma20 > bars[0].close", exit: "bars[0].ma20 < bars[0].close"}
    weight_each: 0.1
`))
	require.NoError(t, err)
	compiled, err := (Compiler{InputFields: compileFields()}).Compile(context.Background(), dsl, "space-1")
	require.NoError(t, err)
	require.Equal(t, "demo", compiled.Name)
	require.Len(t, compiled.Rules, 2)
	require.Equal(t, "ranked", compiled.Rules[0].Name)
	require.NotNil(t, compiled.Rules[0].Score)
	require.NotNil(t, compiled.Rules[1].SignalEntry)
	require.Empty(t, compiled.Rules[0].Score.Dependencies.Bars)
	require.Equal(t, []string{"return_20", "turnover_20"}, compiled.Rules[0].Score.Dependencies.Fields)
}

func TestCompileRejectsSignalScoreReference(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: demo
triggers: {event: {name: ready}}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  r:
    pool: [BTC]
    signals: {entry: "score > 0", exit: "close < 0"}
    weight: 1
`))
	require.NoError(t, err)
	_, err = (Compiler{InputFields: compileFields()}).Compile(context.Background(), dsl, "space-1")
	require.Error(t, err)
	require.ErrorContains(t, err, "score")
}

func TestCompileRequiresSpace(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: demo
triggers: {event: {name: ready}}
data: {bar: 1h, calendar: crypto_24x7}
rules: {r: {pool: [BTC], weight: 1}}
`))
	require.NoError(t, err)
	_, err = (Compiler{}).Compile(context.Background(), dsl, " ")
	require.Error(t, err)
}

func TestCompileInfersScalarFactorFieldsWhenCatalogIsUnavailable(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: demo
triggers: {event: {name: ready}}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  rank: {pool: [BTC], score: bias, weight_each: 1}
`))
	require.NoError(t, err)
	compiled, err := (Compiler{}).Compile(context.Background(), dsl, "space-1")
	require.NoError(t, err)
	require.NotNil(t, compiled.Rules[0].Score)
	require.Contains(t, compiled.Rules[0].Score.Dependencies.Fields, "bias")
}
