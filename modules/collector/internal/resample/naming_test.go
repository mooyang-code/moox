package resample

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultTargetNamesKeepDerivedFrequencyIdentity(t *testing.T) {
	assert.Equal(t, "dataset_spot_kline_derived_4h", DefaultTargetDatasetID("spot", "4h"))
	assert.Equal(t, "dataset_swap_kline_derived_90m", DefaultTargetDatasetID("swap", "90m"))
	assert.Equal(t, "view_spot_kline_derived_4h", DefaultTargetViewID("dataset_spot_kline_derived_4h"))
}

func TestUniqueResampleDisplayNameIsValidAndStable(t *testing.T) {
	name := uniqueResampleDisplayName("dataset_spot_kline_derived_5m")
	if utf8.RuneCountInString(name) > 10 || name == "" {
		t.Fatalf("invalid display name %q", name)
	}
	if name != uniqueResampleDisplayName("dataset_spot_kline_derived_5m") {
		t.Fatal("display name is not stable")
	}
	name842 := uniqueResampleDisplayName("dataset_spot_kline_derived_842m")
	name872 := uniqueResampleDisplayName("dataset_spot_kline_derived_872m")
	if name == name842 || name == name872 || name842 == name872 {
		t.Fatal("distinct target dataset IDs must not share the default display name")
	}
}

func TestValidateTargetDatasetIDRequiresLowerSnakeFrequencySuffix(t *testing.T) {
	require.NoError(t, ValidateTargetDatasetID("dataset_spot_kline_derived_4h", "4h"))
	require.NoError(t, ValidateTargetDatasetID("dataset_x_90m", "90m"))

	tests := []struct {
		name string
		id   string
		slug string
	}{
		{name: "empty", id: "", slug: "4h"},
		{name: "uppercase", id: "dataset_spot_kline_derived_4H", slug: "4h"},
		{name: "dash", id: "spot-kline-derived-4h", slug: "4h"},
		{name: "wrong suffix", id: "dataset_spot_kline_derived_6h", slug: "4h"},
		{name: "suffix without separator", id: "spot_kline_derived4h", slug: "4h"},
		{name: "too long", id: "dataset_" + strings.Repeat("a", 43) + "_4h", slug: "4h"},
		{name: "invalid slug", id: "dataset_spot_kline_derived_1M", slug: "1M"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, ValidateTargetDatasetID(tt.id, tt.slug))
		})
	}
}
