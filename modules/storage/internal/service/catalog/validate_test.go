package catalog

import (
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestValidateDatasetIDAllowsFiftyCharacters(t *testing.T) {
	require.NoError(t, validateDatasetID("a"+strings.Repeat("b", 49)))
	require.Error(t, validateDatasetID("a"+strings.Repeat("b", 50)))
}

func TestValidateViewIDRemainsThirtyCharacters(t *testing.T) {
	require.NoError(t, validateViewID("a"+strings.Repeat("b", 29)))
	require.Error(t, validateViewID("a"+strings.Repeat("b", 30)))
}

func TestValidateColumnDisplayNameAllowsMatchingFactorOutput(t *testing.T) {
	require.NoError(t, validateColumnDisplayName("display_name", "crypto", map[string]string{
		"display_name":  "bias_20",
		"factor_output": "bias_20",
	}, true))
	require.Error(t, validateColumnDisplayName("display_name", "crypto", map[string]string{
		"display_name":  "bias_20",
		"factor_output": "bias_20",
	}, false))
	require.Error(t, validateColumnDisplayName("display_name", "crypto", map[string]string{
		"display_name": "bias_20",
	}, true))
	require.Error(t, validateColumnDisplayName("display_name", "crypto", map[string]string{
		"display_name":  "ma_20",
		"factor_output": "bias_20",
	}, true))
}

func TestFactorColumnIdentity(t *testing.T) {
	attrs := map[string]string{
		"display_name":     "bias_20",
		"factor_output":    "bias_20",
		"origin_factor_id": "bias",
	}
	datasetColumn := &pb.DatasetColumn{
		OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR,
		OriginId:   "bias.bias_20",
		Attributes: attrs,
	}
	require.True(t, isFactorDatasetColumn(datasetColumn))
	datasetColumn.OriginId = "other.bias_20"
	require.False(t, isFactorDatasetColumn(datasetColumn))

	viewColumn := &pb.ViewColumn{
		ColumnName: "result.bias__bias_20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "result.bias__bias_20",
		Attributes: attrs,
	}
	require.True(t, isFactorViewColumn(viewColumn))
	viewColumn.OriginId = "result.other__bias_20"
	require.False(t, isFactorViewColumn(viewColumn))
}
