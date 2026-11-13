package catalog

import (
	"strings"
	"testing"

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

func TestValidateColumnDisplayNameOnlyRequiresPresence(t *testing.T) {
	// 显示名由建列的模块写入，Storage 只要求有，不按业务规则推断。
	require.NoError(t, validateColumnDisplayName("display_name", map[string]string{"display_name": "bias__20"}))
	require.NoError(t, validateColumnDisplayName("display_name", map[string]string{"display_name": "收盘价"}))
	require.Error(t, validateColumnDisplayName("display_name", map[string]string{}))
	require.Error(t, validateColumnDisplayName("display_name", map[string]string{"display_name": "  "}))
	require.Error(t, validateColumnDisplayName("display_name", nil))
}

// 数据集列名会原样成为 View 索引里的列：系统列名与非标识符被拒绝。
func TestDatasetColumnNameRules(t *testing.T) {
	for _, name := range []string{"close", "bias__20", "__params", "Close_1"} {
		require.NoError(t, validateDatasetColumnName(name), name)
	}
	for _, name := range []string{"subject_id", "Data_Time", "series_tag", "freq", "__moox_attributes", "1close", "a.b", "a-b", ""} {
		require.Error(t, validateDatasetColumnName(name), name)
	}
}
