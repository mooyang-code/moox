package primarystore

import (
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultViewHelpers(t *testing.T) {
	assert.Equal(t, []string{"subject_id", "freq", "data_time"}, defaultViewGrainKeys(pb.DataKind_DATA_KIND_TIME_SERIES))
	assert.Equal(t, []string{"record_id", "version"}, defaultViewGrainKeys(pb.DataKind_DATA_KIND_RECORD))
	assert.Equal(t, "duckdb", defaultViewEngine(pb.DataKind_DATA_KIND_TIME_SERIES))
	assert.Equal(t, "bleve", defaultViewEngine(pb.DataKind_DATA_KIND_RECORD))
}

func TestDatasetSupportsFreq(t *testing.T) {
	dataset := &pb.Dataset{Freqs: []string{"1m", "1h"}}
	assert.True(t, datasetSupportsFreq(dataset, "1m"))
	assert.False(t, datasetSupportsFreq(dataset, "1d"))
	assert.False(t, datasetSupportsFreq(dataset, " 1m "))
}

func TestValidateViewIDRejectsInvalidValues(t *testing.T) {
	require.NoError(t, validateViewID("news_view"))
	require.Error(t, validateViewID(""))
	require.Error(t, validateViewID("NewsView"))
}

func TestValidateChineseDisplayNameRequiresHanCharacters(t *testing.T) {
	require.NoError(t, validateChineseDisplayName("display_name", "新闻视图"))
	require.Error(t, validateChineseDisplayName("display_name", ""))
	require.Error(t, validateChineseDisplayName("display_name", "news-only"))
}

func TestValidateColumnDisplayNameAllowsInternalOperationalLabels(t *testing.T) {
	require.NoError(t, validateColumnDisplayName("display_name", "moox_system", map[string]string{
		"display_name": "Producer node ID",
	}))
	require.Error(t, validateColumnDisplayName("display_name", "crypto", map[string]string{
		"display_name": "Producer node ID",
	}))
	require.Error(t, validateColumnDisplayName("display_name", "moox_fake", map[string]string{
		"display_name": "Producer node ID",
	}))
}

func TestNormalizeViewDatasetIDsDedupesAndPrefixesPrimary(t *testing.T) {
	got := normalizeViewDatasetIDs("kline", []string{"news", "kline", "", "news"})
	assert.Equal(t, []string{"kline", "news"}, got)
}

func TestValidateViewColumnNameRequiresDatasetOriginFormat(t *testing.T) {
	err := validateViewColumnName(&pb.ViewColumn{
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ColumnName: "kline.close",
	})
	require.NoError(t, err)

	err = validateViewColumnName(&pb.ViewColumn{
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "invalid",
		ColumnName: "kline.close",
	})
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "origin_id")
}

func TestNormalizeTimeSeriesViewFilterJSON(t *testing.T) {
	freq, normalized, err := normalizeTimeSeriesViewFilterJSON(`{"freq":"1m","extra":true}`)
	require.NoError(t, err)
	assert.Equal(t, "1m", freq)
	assert.Contains(t, normalized, `"freq":"1m"`)

	_, _, err = normalizeTimeSeriesViewFilterJSON("")
	require.Error(t, err)

	_, _, err = normalizeTimeSeriesViewFilterJSON(`{"freq":1}`)
	require.Error(t, err)
}
