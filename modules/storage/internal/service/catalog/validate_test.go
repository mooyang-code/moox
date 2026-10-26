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
	// 因子输出列的展示名必须等于列名。
	require.NoError(t, validateColumnDisplayName("display_name", "crypto", "bias__20", map[string]string{"display_name": "bias__20"}, true))
	require.Error(t, validateColumnDisplayName("display_name", "crypto", "bias__20", map[string]string{"display_name": "bias__20"}, false),
		"不是因子输出列时，展示名必须含中文")
	require.Error(t, validateColumnDisplayName("display_name", "crypto", "bias__20", map[string]string{"display_name": "ma__20"}, true))
	require.Error(t, validateColumnDisplayName("display_name", "crypto", "bias__20", map[string]string{}, true))
	// 参数列 __params 按普通业务列处理：含中文且不超过 10 个字。
	require.NoError(t, validateColumnDisplayName("display_name", "crypto", "__params", map[string]string{"display_name": "因子参数"}, false))
}

func TestFactorResultDatasetRecognitionUsesRole(t *testing.T) {
	require.True(t, isFactorResultDataset(&pb.Dataset{Attributes: map[string]string{"dataset_role": "factor_result"}}))
	require.True(t, isFactorResultDataset(&pb.Dataset{Attributes: map[string]string{"dataset_role": " Factor_Result "}}))
	require.False(t, isFactorResultDataset(&pb.Dataset{Attributes: map[string]string{"dataset_role": "raw_collection"}}))
	require.False(t, isFactorResultDataset(nil))
}

func TestFactorViewColumnIdentity(t *testing.T) {
	attrs := map[string]string{"display_name": "bias__20", "column_kind": "output", "member_key": "bias__20", "factor_id": "bias", "output_name": "bias"}
	viewColumn := &pb.ViewColumn{
		ColumnName: "bias__20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "bias__20",
		Attributes: attrs,
	}
	require.True(t, isFactorViewColumn(viewColumn))
	viewColumn.OriginId = "bias_q__20"
	require.False(t, isFactorViewColumn(viewColumn), "column_name must equal origin_id")
	viewColumn.ColumnName, viewColumn.OriginId = "bias_q__20", "bias_q__20"
	delete(attrs, "member_key")
	require.False(t, isFactorViewColumn(viewColumn), "an output column needs member_key")
	attrs["member_key"], attrs["column_kind"] = "bias__20", "params"
	require.False(t, isFactorViewColumn(viewColumn), "the params column is not a factor output")
	viewColumn.OriginType = pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM
	viewColumn.ColumnName, viewColumn.OriginId = "bias_20", "bias_20"
	require.False(t, isFactorViewColumn(viewColumn))
}

func TestDatasetViewColumnUsesTheBareColumnName(t *testing.T) {
	column := &pb.ViewColumn{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "close"}
	require.NoError(t, validateViewColumnName(column))
	column.ColumnName, column.OriginId = "dataset_prices.close", "dataset_prices.close"
	require.ErrorContains(t, validateViewColumnName(column), "bare Dataset column name")
	column.ColumnName, column.OriginId = "close", "open"
	require.ErrorContains(t, validateViewColumnName(column), "column_name must equal origin_id")
	column.ColumnName, column.OriginId = "data_time", "data_time"
	require.ErrorContains(t, validateViewColumnName(column), "reserved system column")
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

func TestValidateColumnFill(t *testing.T) {
	for _, ok := range []string{"", "prev", "prev_close", "zero", " zero "} {
		if err := validateColumnFill(map[string]string{ColumnFillAttribute: ok}); err != nil {
			t.Fatalf("fill %q should be accepted: %v", ok, err)
		}
	}
	if err := validateColumnFill(nil); err != nil {
		t.Fatalf("nil attributes: %v", err)
	}
	if err := validateColumnFill(map[string]string{ColumnFillAttribute: "next"}); err == nil {
		t.Fatal("unknown fill rule must be rejected")
	}
}
