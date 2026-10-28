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

func TestValidateColumnDisplayNameOnlyRequiresPresence(t *testing.T) {
	// 显示名由建列的模块写入，Storage 只要求有，不按业务规则推断。
	require.NoError(t, validateColumnDisplayName("display_name", map[string]string{"display_name": "bias__20"}))
	require.NoError(t, validateColumnDisplayName("display_name", map[string]string{"display_name": "收盘价"}))
	require.Error(t, validateColumnDisplayName("display_name", map[string]string{}))
	require.Error(t, validateColumnDisplayName("display_name", map[string]string{"display_name": "  "}))
	require.Error(t, validateColumnDisplayName("display_name", nil))
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
