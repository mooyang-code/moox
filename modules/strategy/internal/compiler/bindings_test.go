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
