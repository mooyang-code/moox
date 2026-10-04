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
	require.NoError(t, validateViewID("view_"+"a"+strings.Repeat("b", 122)))
	require.Error(t, validateViewID("view_"+"a"+strings.Repeat("b", 123)))
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

func TestFactorResultDatasetRecognitionUsesRole(t *testing.T) {
	require.True(t, isFactorResultDataset(&pb.Dataset{Attributes: map[string]string{"dataset_role": "factor_result"}}))
	require.True(t, isFactorResultDataset(&pb.Dataset{Attributes: map[string]string{"dataset_role": " Factor_Result "}}))
	require.False(t, isFactorResultDataset(&pb.Dataset{Attributes: map[string]string{"dataset_role": "raw_collection"}}))
	require.False(t, isFactorResultDataset(nil))
}

func TestFactorViewColumnIdentity(t *testing.T) {
	attrs := map[string]string{"display_name": "bias_20", "factor_output": "bias_20", "origin_factor_id": "bias"}
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

func TestNormalizeDatasetRoleCanonicalisesAndEnforcesContract(t *testing.T) {
	dataset := &pb.Dataset{DatasetId: "dataset_factor_bars_1m", Attributes: map[string]string{"dataset_role": " Factor_Result "}}
	require.NoError(t, normalizeDatasetRole(dataset))
	require.Equal(t, "factor_result", dataset.Attributes["dataset_role"])

	misnamed := &pb.Dataset{DatasetId: "dataset_bars_1m", Attributes: map[string]string{"dataset_role": "factor_result"}}
	require.ErrorContains(t, normalizeDatasetRole(misnamed), "must start with dataset_factor_")

	retired := &pb.Dataset{DatasetId: "dataset_bars_1m", Attributes: map[string]string{"dataset_role": "merged_factor"}}
	require.ErrorContains(t, normalizeDatasetRole(retired), "retired")

	raw := &pb.Dataset{DatasetId: "dataset_bars_1m", Attributes: map[string]string{"dataset_role": "raw_collection"}}
	require.NoError(t, normalizeDatasetRole(raw))
	require.NoError(t, normalizeDatasetRole(&pb.Dataset{DatasetId: "dataset_plain"}))
	require.NoError(t, normalizeDatasetRole(nil))
}
