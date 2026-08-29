package resample

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultTargetNamesKeepDerivedFrequencyIdentity(t *testing.T) {
	assert.Equal(t, "spot_kline_derived_4h", DefaultTargetDatasetID("spot", "4h"))
	assert.Equal(t, "swap_kline_derived_90m", DefaultTargetDatasetID("swap", "90m"))
	assert.Equal(t, "spot_kline_derived_4h_view", DefaultTargetViewID("spot_kline_derived_4h"))
}

func TestUniqueResampleDisplayNameIsValidAndStable(t *testing.T) {
	name := uniqueResampleDisplayName("spot_kline_derived_5m")
	if utf8.RuneCountInString(name) > 10 || name == "" {
		t.Fatalf("invalid display name %q", name)
	}
	if name != uniqueResampleDisplayName("spot_kline_derived_5m") {
		t.Fatal("display name is not stable")
	}
}

func TestValidateTargetDatasetIDRequiresLowerSnakeFrequencySuffix(t *testing.T) {
	require.NoError(t, ValidateTargetDatasetID("spot_kline_derived_4h", "4h"))
	require.NoError(t, ValidateTargetDatasetID("x_90m", "90m"))

	tests := []struct {
		name string
		id   string
		slug string
	}{
		{name: "empty", id: "", slug: "4h"},
		{name: "uppercase", id: "spot_kline_derived_4H", slug: "4h"},
		{name: "dash", id: "spot-kline-derived-4h", slug: "4h"},
		{name: "wrong suffix", id: "spot_kline_derived_6h", slug: "4h"},
		{name: "suffix without separator", id: "spot_kline_derived4h", slug: "4h"},
		{name: "too long", id: "perpetual_kline_derived_4h", slug: "4h"},
		{name: "invalid slug", id: "spot_kline_derived_1M", slug: "1M"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, ValidateTargetDatasetID(tt.id, tt.slug))
		})
	}
}
