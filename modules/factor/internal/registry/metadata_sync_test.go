package registry

import (
	"testing"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSafeID(t *testing.T) {
	assert.Equal(t, "abc-123", safeID("Abc 123"))
	assert.Equal(t, "hello-world", safeID("Hello__World!!"))
	assert.Equal(t, "", safeID("!!!"))
}

func TestFactorParams(t *testing.T) {
	got, err := factorParams("")
	require.NoError(t, err)
	assert.Empty(t, got)

	got, err = factorParams("[1,2,3]")
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, got)

	_, err = factorParams("{bad")
	require.Error(t, err)
}

func TestDataSourceIDFromDataset(t *testing.T) {
	assert.Equal(t, "binance", DataSourceIDFromDataset("binance_spot_kline"))
	assert.Equal(t, "alone", DataSourceIDFromDataset("alone"))
	assert.Equal(t, "", DataSourceIDFromDataset(""))
}

func TestColumnAndDatasetDisplayName(t *testing.T) {
	assert.Equal(t, "因子14", columnDisplayName("sma_14"))
	assert.Equal(t, "因子", columnDisplayName("nounderscore"))
	name := datasetDisplayName("binance_spot_kline")
	assert.Contains(t, name, "因子")
	assert.LessOrEqual(t, len([]rune(name)), 10)
}

func TestFactorResultAttributesAndMerge(t *testing.T) {
	attrs := factorResultDatasetAttributes("src_ds", "1m")
	assert.Equal(t, "factor", attrs["owner_module"])
	assert.Equal(t, "src_ds", attrs["source_dataset_id"])

	merged, err := mergeFactorResultDatasetAttributes("ds", nil, "src_ds", "1m")
	require.NoError(t, err)
	assert.Equal(t, "factor_result", merged["dataset_role"])

	_, err = mergeFactorResultDatasetAttributes("ds", map[string]string{
		"owner_module": "other",
	}, "src_ds", "1m")
	require.Error(t, err)

	keepFreq, err := mergeFactorResultDatasetAttributes("ds", map[string]string{
		"owner_module":      "factor",
		"dataset_role":      "factor_result",
		"managed_by":        "factor",
		"source_dataset_id": "src_ds",
		"source_freq":       "5m",
	}, "src_ds", "1m")
	require.NoError(t, err)
	assert.Equal(t, "5m", keepFreq["source_freq"])
}

func TestMergeDatasetFreq(t *testing.T) {
	assert.Equal(t, []string{"1m"}, mergeDatasetFreq(nil, "1m"))
	assert.Equal(t, []string{"1m"}, mergeDatasetFreq([]string{"1m"}, "1m"))
	assert.Equal(t, []string{"1m", "5m"}, mergeDatasetFreq([]string{"1m"}, "5m"))
	assert.Equal(t, []string{"1m"}, mergeDatasetFreq([]string{"1m"}, ""))
}

func TestFactorColumnOriginAndStatus(t *testing.T) {
	assert.Equal(t, "sma_14", factorColumnOriginID("sma", 14))
	assert.Equal(t, "disabled", storageFactorStatus(domain.FactorStatusDisabled))
	assert.Equal(t, "active", storageFactorStatus("enabled"))
}

func TestRetInfoHelpers(t *testing.T) {
	assert.True(t, retOK(nil))
	assert.True(t, retOK(&commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}))
	assert.False(t, retOK(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM}))

	assert.False(t, isDuplicateRet(nil))
	assert.True(t, isDuplicateRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "duplicate key"}))
	assert.True(t, isDuplicateRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "Already Exists"}))
	assert.False(t, isDuplicateRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "duplicate"}))

	assert.False(t, isDatasetNotFoundRet(nil))
	assert.True(t, isDatasetNotFoundRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND}))
	assert.True(t, isDatasetNotFoundRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "dataset not found"}))

	assert.False(t, isRefreshInProgressRet(nil))
	assert.True(t, isRefreshInProgressRet(&commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "refresh already in progress"}))

	assert.Nil(t, retInfoError("op", nil))
	err := retInfoError("CreateFactor", &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CreateFactor")
}

func TestStringMapAndSliceHelpers(t *testing.T) {
	assert.Nil(t, cloneStringMap(nil))
	assert.Nil(t, cloneStringMap(map[string]string{}))
	cloned := cloneStringMap(map[string]string{"a": "1"})
	require.Equal(t, map[string]string{"a": "1"}, cloned)
	cloned["a"] = "2"
	assert.Equal(t, "1", map[string]string{"a": "1"}["a"])

	assert.True(t, stringMapsEqual(nil, nil))
	assert.False(t, stringMapsEqual(map[string]string{"a": "1"}, nil))
	assert.True(t, stringMapsEqual(map[string]string{"a": "1"}, map[string]string{"a": "1"}))
	assert.False(t, stringMapsEqual(map[string]string{"a": "1"}, map[string]string{"a": "2"}))

	assert.True(t, stringSlicesEqual(nil, nil))
	assert.False(t, stringSlicesEqual([]string{"a"}, nil))
	assert.True(t, stringSlicesEqual([]string{"a", "b"}, []string{"a", "b"}))
	assert.False(t, stringSlicesEqual([]string{"a"}, []string{"b"}))
}
