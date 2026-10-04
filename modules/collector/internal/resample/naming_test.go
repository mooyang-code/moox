package resample

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

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

func TestValidateTargetDatasetIDRequiresTaskOwnedLowerSnakeIdentity(t *testing.T) {
	require.NoError(t, ValidateTargetDatasetID("dataset_collector_0123456789abcdef", "4h"))

	tests := []struct {
		name string
		id   string
	}{
		{name: "empty", id: ""},
		{name: "uppercase", id: "dataset_collector_0123456789ABCDEF"},
		{name: "dash", id: "dataset-collector-0123456789abcdef"},
		{name: "missing prefix", id: "collector_0123456789abcdef"},
		{name: "too long", id: "dataset_" + strings.Repeat("a", 44)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, ValidateTargetDatasetID(tt.id))
		})
	}
}
