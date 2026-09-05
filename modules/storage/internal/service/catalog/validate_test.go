package catalog

import (
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestValidateDatasetIDAllowsFiftyCharacters(t *testing.T) {
	require.NoError(t, validateDatasetID("dataset_a"+strings.Repeat("b", 41)))
	require.Error(t, validateDatasetID("dataset_a"+strings.Repeat("b", 42)))
	require.ErrorContains(t, validateDatasetID("a"+strings.Repeat("b", 49)), "must start with dataset_")
}

func TestValidateViewIDAllowsFiftyCharacters(t *testing.T) {
	require.NoError(t, validateViewID("view_"+"a"+strings.Repeat("b", 44)))
	require.Error(t, validateViewID("view_"+"a"+strings.Repeat("b", 45)))
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
	viewColumn.ColumnName = "result.bias__bias_20"
	viewColumn.OriginId = "result.bias__bias_20"
	viewColumn.Attributes["origin_factor_id"] = "Bias"
	require.True(t, isFactorViewColumn(viewColumn))
	viewColumn.OriginId = "result.other__bias_20"
	require.False(t, isFactorViewColumn(viewColumn))
}
