package compiler

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCompileWithBindingsExposesFactorFieldsToBars(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: ma-cross
triggers: {event: {name: factor.ready}}
data: {bar: 1d, calendar: cn_stock}
rules:
  signal:
    pool: [000001.SZ]
    signals:
      entry: "bars[-1].ma20 <= bars[-1].close && bars[0].ma20 > bars[0].close"
      exit: "bars[-1].ma20 >= bars[-1].close && bars[0].ma20 < bars[0].close"
    weight_each: 1
`))
	require.NoError(t, err)
	compiled, err := (Compiler{}).CompileWithBindings(context.Background(), dsl, "space-1", []byte(`{
  "source_view_id":"source",
  "factors":[{"factor_id":"ma20_factor","binding_id":"binding","frequency":"1d","result_dataset_id":"result","result_view_id":"result-view","output":"value","column_name":"ma20"}]
}`))
	require.NoError(t, err)
	require.Len(t, compiled.Factors, 1)
	require.NotNil(t, compiled.Rules[0].SignalEntry)
	require.Contains(t, compiled.Rules[0].SignalEntry.Dependencies.Bars[-1], "ma20")
	require.Equal(t, "source", compiled.SourceView.ID)
}

func TestCompileWithBindingsRejectsSourceReadyWithFactorBindings(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: source-factor
triggers: {event: {name: source.ready}}
data: {bar: 1d, calendar: cn_stock}
rules: {r: {pool: [000001.SZ], score: close, weight: 1}}
`))
	require.NoError(t, err)
	_, err = (Compiler{}).CompileWithBindings(context.Background(), dsl, "space-1", []byte(`{
  "source_view_id":"source",
  "factors":[{"factor_id":"ma20","result_view_id":"result","column_name":"ma20"}]
}`))
	require.ErrorContains(t, err, "source.ready cannot be used with factor bindings")
}

func TestCompileWithBindingsRejectsScheduleOnlyFactorStrategy(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: schedule-factor
triggers: {schedule: {cron: "@daily"}}
data: {bar: 1d, calendar: cn_stock}
rules: {r: {pool: [000001.SZ], score: ma20, weight: 1}}
`))
	require.NoError(t, err)
	_, err = (Compiler{}).CompileWithBindings(context.Background(), dsl, "space-1", []byte(`{
  "source_view_id":"source",
  "factors":[{"factor_id":"ma20","result_view_id":"result","column_name":"ma20"}]
}`))
	require.ErrorContains(t, err, "require a factor.ready event trigger")
}

func TestCompileWithBindingsAllowsSourceReadyWithoutFactorBindings(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: source-only
triggers: {event: {name: source.ready}}
data: {bar: 1d, calendar: cn_stock}
rules: {r: {pool: [000001.SZ], score: close, weight: 1}}
`))
	require.NoError(t, err)
	_, err = (Compiler{}).CompileWithBindings(context.Background(), dsl, "space-1", []byte(`{"source_view_id":"source"}`))
	require.NoError(t, err)
}

func TestCompileWithBindingsRejectsFrequencyDifferentFromDSLBar(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: mismatch
triggers: {event: {name: factor.ready}}
data: {bar: 1h, calendar: crypto_24x7}
rules: {r: {pool: [BTC], score: close, weight: 1}}
`))
	require.NoError(t, err)
	_, err = (Compiler{}).CompileWithBindings(context.Background(), dsl, "space-1", []byte(`{"source_view_id":"source","frequency":"5m"}`))
	require.Error(t, err)
}

func TestCompileWithBindingsRejectsUndeclaredScalarField(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: typo
triggers: {event: {name: factor.ready}}
data: {bar: 1h, calendar: crypto_24x7}
rules: {r: {pool: [BTC], score: misspelled_factor, weight: 1}}
`))
	require.NoError(t, err)
	_, err = (Compiler{}).CompileWithBindings(context.Background(), dsl, "space-1", []byte(`{"source_view_id":"source"}`))
	require.ErrorContains(t, err, "not declared by instance bindings")
}

func TestCompileWithBindingsRejectsAmbiguousFactorAliases(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: duplicate
triggers: {event: {name: factor.ready}}
data: {bar: 1h, calendar: crypto_24x7}
rules: {r: {pool: [BTC], score: value, weight: 1}}
`))
	require.NoError(t, err)
	_, err = (Compiler{}).CompileWithBindings(context.Background(), dsl, "space-1", []byte(`{
  "source_view_id":"source",
  "factors":[
    {"factor_id":"one","output":"value","column_name":"one","result_view_id":"result"},
    {"factor_id":"two","output":"value","column_name":"two","result_view_id":"result"}
  ]
}`))
	require.ErrorContains(t, err, "used by multiple factors")
}

func TestCompileWithBindingsRejectsMultipleResultViews(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: views
triggers: {event: {name: factor.ready}}
data: {bar: 1h, calendar: crypto_24x7}
rules: {r: {pool: [BTC], score: close, weight: 1}}
`))
	require.NoError(t, err)
	_, err = (Compiler{}).CompileWithBindings(context.Background(), dsl, "space-1", []byte(`{
  "source_view_id":"source",
  "factors":[
    {"factor_id":"one","output":"value","column_name":"one","result_view_id":"result-a"},
    {"factor_id":"two","output":"other","column_name":"two","result_view_id":"result-b"}
  ]
}`))
	require.ErrorContains(t, err, "must share one result_view_id")
}

func TestCompileWithBindingsRejectsFactorAliasShadowingSourceInput(t *testing.T) {
	dsl, err := config.Parse([]byte(`name: shadow
triggers: {event: {name: factor.ready}}
data: {bar: 1h, calendar: crypto_24x7}
rules: {r: {pool: [BTC], score: value, weight: 1}}
`))
	require.NoError(t, err)
	_, err = (Compiler{}).CompileWithBindings(context.Background(), dsl, "space-1", []byte(`{
  "source_view_id":"source",
  "factors":[
    {"factor_id":"one","input_columns":["turnover_20"],"output":"value","column_name":"one","result_view_id":"result"},
    {"factor_id":"turnover_20","input_columns":["close"],"output":"other","column_name":"two","result_view_id":"result"}
  ]
}`))
	require.ErrorContains(t, err, "conflicts with a source input column")
}
