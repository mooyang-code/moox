package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func validManifestYAML() []byte {
	return []byte(`api_version: moox.strategy/v2
kind: coin_selection
input:
  source_view_id: view_crypto_spot_kline_1h
  data_frequency: 1h
  factors:
    - factor_id: Bias
instrument_pool:
  exchanges: [binance]
  markets: [spot]
  quote_assets: [USDT]
  min_history_periods: 24
schedule:
  every: 24h
readiness:
  policy: strict
long:
  side_weight: "1"
  scores:
    - factor_id: Bias
      direction: ascending
      weight: "1"
  filters:
    - phase: pre
      factor_id: Bias
      value_type: value
      op: gt
      value: "0"
  selection:
    mode: count
    value: "2"
`)
}

func TestParseManifestNormalizesDefaults(t *testing.T) {
	manifest, err := Parse(validManifestYAML())
	require.NoError(t, err)
	require.Equal(t, APIVersion, manifest.APIVersion)
	require.Equal(t, "24H", manifest.Schedule.Every)
	require.Equal(t, "strict", manifest.Readiness.Policy)
}

func TestParseManifestRejectsUnknownAndUnsupportedFields(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("api_version: moox.strategy/v2\nkind: coin_selection\nunknown: true\n"),
		[]byte("api_version: moox.strategy/v1\nkind: coin_selection\n"),
		[]byte("api_version: moox.strategy/v2\nkind: python\n"),
	} {
		_, err := Parse(raw)
		require.Error(t, err)
	}
}

func TestParseManifestRejectsPureSpotShort(t *testing.T) {
	manifest, err := Parse(validManifestYAML())
	require.NoError(t, err)
	manifest.Short = &Side{
		SideWeight: "1",
		Scores:     []ScoreRule{{FactorID: "Bias", Direction: "descending", Weight: "1"}},
		Selection:  SelectionRule{Mode: "count", Value: "1"},
	}
	manifest.InstrumentPool.Markets = []string{"spot"}
	require.ErrorContains(t, Validate(&manifest), "spot-only instrument_pool cannot enable short")
}

func TestParseManifestRejectsInvalidScheduleAndSelection(t *testing.T) {
	manifest, err := Parse(validManifestYAML())
	require.NoError(t, err)
	manifest.Schedule.Every = "90m"
	require.Error(t, Validate(&manifest))
	manifest, err = Parse(validManifestYAML())
	require.NoError(t, err)
	manifest.Long.Selection = SelectionRule{Mode: "fraction", Value: "1.1"}
	require.Error(t, Validate(&manifest))
	manifest, err = Parse(validManifestYAML())
	require.NoError(t, err)
	manifest.Long.Selection = SelectionRule{Mode: "fraction", Value: "-0.1"}
	require.Error(t, Validate(&manifest))
	manifest, err = Parse(validManifestYAML())
	require.NoError(t, err)
	manifest.Long.SideWeight = "0"
	require.ErrorContains(t, Validate(&manifest), "positive long or short side_weight")
}

func TestParseManifestAllowsExplicitMultiOutputSelection(t *testing.T) {
	raw := validManifestYAML()
	raw = append(raw, []byte("\n")...)
	manifest, err := Parse(raw)
	require.NoError(t, err)
	manifest.Input.Factors[0].Output = "bias_20"
	require.NoError(t, Validate(&manifest))
}
